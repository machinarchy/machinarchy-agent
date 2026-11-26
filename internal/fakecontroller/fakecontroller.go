// Package fakecontroller is an in-process implementation of the controller
// side of the agent gateway (ADR-059): the gRPC service the real controller
// serves, over signed agentv1 envelopes. Integration tests run the real agent
// against it, and it doubles as an executable reference for the machinarchy
// controller: enrolment, signed responses, lease-bound command streams,
// at-least-once command delivery, and result verification.
package fakecontroller

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// Controller is a minimal controller for tests.
type Controller struct {
	Key             ed25519.PrivateKey
	EnrollmentToken string
	LeaseTTL        time.Duration
	Heartbeat       time.Duration
	// AckWait is how long a delivered command may stay unacknowledged before
	// it is delivered again.
	AckWait time.Duration

	tlsConfig *tls.Config
	addr      string

	mu            sync.Mutex
	server        *grpc.Server
	agents        map[string]*agent
	registrations map[string]string // request ID -> agent ID
	seen          map[string]bool   // command IDs ever queued (de-duplication)
	results       chan agentv1.Result
	seq           atomic.Int64
	errs          chan error

	// RevokeRenewals makes lease renewals fail with lease_revoked.
	RevokeRenewals atomic.Bool
	// AutoAck acknowledges terminal results as they arrive.
	AutoAck atomic.Bool
}

type agent struct {
	key        ed25519.PublicKey
	leaseID    string
	leaseUntil time.Time
	queue      [][]byte
	wake       chan struct{}
}

// Option configures Start.
type Option func(*Controller)

// WithTLS serves over TLS with the given configuration.
func WithTLS(config *tls.Config) Option { return func(c *Controller) { c.tlsConfig = config } }

// WithAddress listens on addr (host:port) instead of a free loopback port.
func WithAddress(addr string) Option { return func(c *Controller) { c.addr = addr } }

// Start listens and serves the agent gateway until Close.
func Start(_ context.Context, key ed25519.PrivateKey, enrollmentToken string, options ...Option) (*Controller, error) {
	c := &Controller{Key: key, EnrollmentToken: enrollmentToken, LeaseTTL: time.Minute, Heartbeat: 5 * time.Second,
		AckWait: 2 * time.Second, addr: "127.0.0.1:0", agents: map[string]*agent{}, registrations: map[string]string{},
		seen: map[string]bool{}, results: make(chan agentv1.Result, 256), errs: make(chan error, 64)}
	c.AutoAck.Store(true)
	for _, option := range options {
		option(c)
	}
	if err := c.Resume(); err != nil {
		return nil, err
	}
	return c, nil
}

// URL is the server URL agents are configured with.
func (c *Controller) URL() string {
	scheme := "http"
	if c.tlsConfig != nil {
		scheme = "https"
	}
	return scheme + "://" + c.addr
}

// Resume starts serving again after Stop, on the same address and with all
// enrolments and queued commands intact (a controller restart or a network
// that comes back).
func (c *Controller) Resume() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.server != nil {
		return nil
	}
	l, err := net.Listen("tcp", c.addr)
	if err != nil {
		return err
	}
	c.addr = l.Addr().String()
	options := []grpc.ServerOption{grpc.ForceServerCodecV2(codec{}), grpc.MaxRecvMsgSize(agentv1.MaxResultMessageBytes)}
	if c.tlsConfig != nil {
		options = append(options, grpc.Creds(credentials.NewTLS(c.tlsConfig)))
	}
	server := grpc.NewServer(options...)
	server.RegisterService(&serviceDesc, c)
	c.server = server
	go func() {
		if err := server.Serve(l); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			c.report(err)
		}
	}()
	return nil
}

// Stop drops every connection and stops answering until Resume.
func (c *Controller) Stop() {
	c.mu.Lock()
	server := c.server
	c.server = nil
	c.mu.Unlock()
	if server != nil {
		server.Stop()
	}
}

// Close stops serving.
func (c *Controller) Close() { c.Stop() }

// Errors exposes internal failures (bounded; extra errors are dropped).
func (c *Controller) Errors() <-chan error { return c.errs }

func (c *Controller) report(err error) {
	if err == nil {
		return
	}
	select {
	case c.errs <- err:
	default:
	}
}

// Results delivers verified results in arrival order.
func (c *Controller) Results() <-chan agentv1.Result { return c.results }

// Agents returns registered agent IDs.
func (c *Controller) Agents() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]string, 0, len(c.agents))
	for id := range c.agents {
		ids = append(ids, id)
	}
	return ids
}

func (c *Controller) keyFor(agentID string) (ed25519.PublicKey, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.agents[agentID]
	if !ok {
		return nil, false
	}
	return a.key, true
}

func (c *Controller) seal(kind agentv1.Kind, body agentv1.Body) (*frame, error) {
	payload, err := agentv1.Seal(c.Key, kind, body)
	if err != nil {
		c.report(err)
		return nil, status.Error(codes.Internal, "cannot sign the answer")
	}
	return &frame{data: payload}, nil
}

var errInvalid = status.Error(codes.InvalidArgument, "invalid request")

func (c *Controller) lease() *agentv1.Lease {
	return &agentv1.Lease{LeaseID: fmt.Sprintf("lease-%d", c.seq.Add(1)), ExpiresAt: time.Now().Add(c.LeaseTTL).UTC(),
		HeartbeatIntervalMillis: c.Heartbeat.Milliseconds()}
}

func (c *Controller) register(_ context.Context, in *frame) (*frame, error) {
	request, err := agentv1.OpenRegisterRequest(in.data)
	if err != nil {
		return nil, errInvalid // unauthenticated: no signed answer
	}
	response := &agentv1.RegisterResponse{ProtocolVersion: agentv1.ProtocolVersion, RequestID: request.RequestID}
	if subtle.ConstantTimeCompare([]byte(request.EnrollmentToken), []byte(c.EnrollmentToken)) != 1 {
		response.Error = &agentv1.Error{Code: agentv1.ErrorEnrollmentRejected}
		return c.seal(agentv1.KindRegisterResponse, response)
	}
	lease := c.lease()
	c.mu.Lock()
	agentID, retried := c.registrations[request.RequestID]
	if !retried {
		agentID = "agent-" + hex.EncodeToString(request.AgentPublicKey[:8])
		c.registrations[request.RequestID] = agentID
		c.agents[agentID] = &agent{key: ed25519.PublicKey(request.AgentPublicKey), wake: make(chan struct{}, 1)}
	}
	c.agents[agentID].leaseID, c.agents[agentID].leaseUntil = lease.LeaseID, lease.ExpiresAt
	c.mu.Unlock()
	response.AgentID, response.HostID, response.Lease = agentID, "host-"+agentID, lease
	return c.seal(agentv1.KindRegisterResponse, response)
}

func (c *Controller) renew(_ context.Context, in *frame) (*frame, error) {
	var request agentv1.RenewRequest
	if err := agentv1.OpenFromAgent(agentv1.KindRenewRequest, in.data, agentv1.MaxControlMessageBytes, c.keyFor, &request); err != nil {
		return nil, errInvalid
	}
	response := &agentv1.RenewResponse{ProtocolVersion: agentv1.ProtocolVersion, RequestID: request.RequestID, AgentID: request.AgentID}
	if c.RevokeRenewals.Load() {
		response.Error = &agentv1.Error{Code: agentv1.ErrorLeaseRevoked}
	} else {
		response.Lease = c.lease()
		c.mu.Lock()
		c.agents[request.AgentID].leaseID, c.agents[request.AgentID].leaseUntil = response.Lease.LeaseID, response.Lease.ExpiresAt
		c.mu.Unlock()
	}
	return c.seal(agentv1.KindRenewResponse, response)
}

func (c *Controller) sendResult(_ context.Context, in *frame) (*frame, error) {
	var result agentv1.Result
	if err := agentv1.OpenFromAgent(agentv1.KindResult, in.data, agentv1.MaxResultMessageBytes, c.keyFor, &result); err != nil {
		return nil, status.Error(codes.Unauthenticated, "not authorized")
	}
	select {
	case c.results <- result:
	default:
	}
	if result.Status.Terminal() && c.AutoAck.Load() {
		c.report(c.Send(context.Background(), result.AgentID, agentv1.Command{Type: agentv1.CommandResultAck, OperationID: result.OperationID}))
	}
	return &frame{}, nil
}

// commands serves one stream: the opening request proves identity and lease,
// then queued commands are delivered one at a time until acknowledged.
func (c *Controller) commands(stream grpc.ServerStream) error {
	ctx := stream.Context()
	var open frame
	if err := stream.RecvMsg(&open); err != nil {
		return err
	}
	var request agentv1.RenewRequest
	if err := agentv1.OpenFromAgent(agentv1.KindRenewRequest, open.data, agentv1.MaxControlMessageBytes, c.keyFor, &request); err != nil {
		return status.Error(codes.Unauthenticated, "not authorized")
	}
	if d := time.Since(request.SentAt); d > agentv1.MaxClockSkew || d < -agentv1.MaxClockSkew {
		return status.Error(codes.Unauthenticated, "not authorized")
	}
	c.mu.Lock()
	a := c.agents[request.AgentID]
	valid := a.leaseID == request.LeaseID && time.Now().Before(a.leaseUntil)
	c.mu.Unlock()
	if !valid {
		return status.Error(codes.PermissionDenied, "lease expired or unknown")
	}
	acks := make(chan string, 1)
	go func() {
		for {
			var in frame
			if err := stream.RecvMsg(&in); err != nil {
				close(acks)
				return
			}
			select {
			case acks <- string(in.data):
			default:
			}
		}
	}()
	for {
		payload, ok := c.head(ctx, a)
		if !ok {
			return ctx.Err()
		}
		if err := stream.SendMsg(&frame{data: payload}); err != nil {
			return err
		}
		select {
		case _, more := <-acks:
			if !more {
				return nil
			}
			c.pop(a, payload)
		case <-time.After(c.AckWait): // not acknowledged: deliver it again
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// head waits for the oldest queued command.
func (c *Controller) head(ctx context.Context, a *agent) ([]byte, bool) {
	for {
		c.mu.Lock()
		if len(a.queue) > 0 {
			payload := a.queue[0]
			c.mu.Unlock()
			return payload, true
		}
		c.mu.Unlock()
		select {
		case <-a.wake:
		case <-ctx.Done():
			return nil, false
		}
	}
}

func (c *Controller) pop(a *agent, payload []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(a.queue) > 0 && string(a.queue[0]) == string(payload) {
		a.queue = a.queue[1:]
	}
}

// Request sends an operation.request command and returns its operation ID.
func (c *Controller) Request(ctx context.Context, agentID, capability string, input any, timeout time.Duration) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	operationID := fmt.Sprintf("op-%d", c.seq.Add(1))
	return operationID, c.Send(ctx, agentID, agentv1.Command{Type: agentv1.CommandOperationRequest, OperationID: operationID,
		Capability: capability, Input: encoded, TimeoutMillis: timeout.Milliseconds()})
}

// Send fills in envelope fields, signs and queues cmd for the agent; a command
// ID queued twice is delivered once.
func (c *Controller) Send(ctx context.Context, agentID string, cmd agentv1.Command) error {
	return c.SendSignedBy(ctx, c.Key, agentID, cmd)
}

// SendSignedBy is Send with an arbitrary key, for forgery tests.
func (c *Controller) SendSignedBy(_ context.Context, key ed25519.PrivateKey, agentID string, cmd agentv1.Command) error {
	now := time.Now().UTC()
	cmd.ProtocolVersion = agentv1.ProtocolVersion
	if cmd.CommandID == "" {
		cmd.CommandID = fmt.Sprintf("cmd-%d", c.seq.Add(1))
	}
	if cmd.AgentID == "" {
		cmd.AgentID = agentID
	}
	if cmd.IssuedAt.IsZero() {
		cmd.IssuedAt, cmd.ExpiresAt = now, now.Add(5*time.Minute)
	}
	payload, err := agentv1.Seal(key, agentv1.KindCommand, &cmd)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.agents[agentID]
	if !ok {
		return fmt.Errorf("unknown agent %q", agentID)
	}
	if c.seen[cmd.CommandID] {
		return nil
	}
	c.seen[cmd.CommandID] = true
	a.queue = append(a.queue, payload)
	select {
	case a.wake <- struct{}{}:
	default:
	}
	return nil
}

// Await waits for a result for operationID matching status.
func (c *Controller) Await(ctx context.Context, operationID string, status agentv1.OperationStatus) (agentv1.Result, error) {
	for {
		select {
		case <-ctx.Done():
			return agentv1.Result{}, fmt.Errorf("waiting for %s %s: %w", operationID, status, ctx.Err())
		case result := <-c.results:
			if result.OperationID == operationID && result.Status == status {
				return result, nil
			}
		}
	}
}

// ErrNoAgent is returned by WaitForAgent when no agent registers in time.
var ErrNoAgent = errors.New("no agent registered")

// WaitForAgent returns the first registered agent ID.
func (c *Controller) WaitForAgent(ctx context.Context) (string, error) {
	for {
		if ids := c.Agents(); len(ids) > 0 {
			return strings.Join(ids, ","), nil
		}
		select {
		case <-ctx.Done():
			return "", ErrNoAgent
		case <-time.After(20 * time.Millisecond):
		}
	}
}
