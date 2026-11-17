// Package fakecontroller is an in-process implementation of the controller
// side of protocol/agentv1. Integration tests run the real agent against it,
// and it doubles as an executable reference for the machinarchy controller:
// enrollment, signed responses, JetStream provisioning, command publishing,
// result verification and acknowledgement.
package fakecontroller

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// Controller is a minimal controller for tests.
type Controller struct {
	Key             ed25519.PrivateKey
	EnrollmentToken string
	LeaseTTL        time.Duration
	Heartbeat       time.Duration

	nc   *nats.Conn
	js   jetstream.JetStream
	subs []*nats.Subscription

	mu            sync.Mutex
	agents        map[string]ed25519.PublicKey // agent ID -> key
	registrations map[string]string            // request ID -> agent ID
	results       chan agentv1.Result
	seq           atomic.Int64
	errs          chan error

	// RevokeRenewals makes lease renewals fail with lease_revoked.
	RevokeRenewals atomic.Bool
	// AutoAck acknowledges terminal results as they arrive.
	AutoAck atomic.Bool
}

// Start connects to NATS, provisions the command stream and serves
// registration, lease renewal and results.
func Start(ctx context.Context, url string, key ed25519.PrivateKey, enrollmentToken string) (*Controller, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: agentv1.CommandStream, Subjects: []string{agentv1.SubjectAllCommands},
		Storage: jetstream.FileStorage, MaxAge: time.Hour, Duplicates: 10 * time.Minute,
	}); err != nil {
		nc.Close()
		return nil, fmt.Errorf("create command stream: %w", err)
	}
	c := &Controller{Key: key, EnrollmentToken: enrollmentToken, LeaseTTL: time.Minute, Heartbeat: 5 * time.Second,
		nc: nc, js: js, agents: map[string]ed25519.PublicKey{}, registrations: map[string]string{},
		results: make(chan agentv1.Result, 256), errs: make(chan error, 64)}
	c.AutoAck.Store(true)
	for subject, handler := range map[string]nats.MsgHandler{
		agentv1.SubjectRegister:   c.onRegister,
		agentv1.SubjectLeaseRenew: c.onRenew,
		agentv1.SubjectAllResults: c.onResult,
	} {
		sub, err := nc.Subscribe(subject, handler)
		if err != nil {
			c.Close()
			return nil, err
		}
		c.subs = append(c.subs, sub)
	}
	return c, nc.Flush()
}

// Close stops serving.
func (c *Controller) Close() {
	for _, sub := range c.subs {
		c.report(sub.Unsubscribe())
	}
	c.nc.Close()
}

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
	key, ok := c.agents[agentID]
	return key, ok
}

func (c *Controller) reply(msg *nats.Msg, kind agentv1.Kind, body agentv1.Body) {
	payload, err := agentv1.Seal(c.Key, kind, body)
	if err != nil {
		c.report(err)
		return
	}
	c.report(msg.Respond(payload))
}

func (c *Controller) lease() *agentv1.Lease {
	return &agentv1.Lease{LeaseID: fmt.Sprintf("lease-%d", c.seq.Add(1)), ExpiresAt: time.Now().Add(c.LeaseTTL).UTC(),
		HeartbeatIntervalMillis: c.Heartbeat.Milliseconds()}
}

func (c *Controller) onRegister(msg *nats.Msg) {
	request, err := agentv1.OpenRegisterRequest(msg.Data)
	if err != nil {
		return // unauthenticated: no response
	}
	response := &agentv1.RegisterResponse{ProtocolVersion: agentv1.ProtocolVersion, RequestID: request.RequestID}
	if subtle.ConstantTimeCompare([]byte(request.EnrollmentToken), []byte(c.EnrollmentToken)) != 1 {
		response.Error = &agentv1.Error{Code: agentv1.ErrorEnrollmentRejected}
		c.reply(msg, agentv1.KindRegisterResponse, response)
		return
	}
	c.mu.Lock()
	agentID, retried := c.registrations[request.RequestID]
	if !retried {
		agentID = "agent-" + hex.EncodeToString(request.AgentPublicKey[:8])
		c.registrations[request.RequestID] = agentID
		c.agents[agentID] = ed25519.PublicKey(request.AgentPublicKey)
	}
	c.mu.Unlock()
	if err := c.provision(agentID); err != nil {
		response.Error = &agentv1.Error{Code: agentv1.ErrorUnavailable, Retryable: true}
		c.reply(msg, agentv1.KindRegisterResponse, response)
		return
	}
	response.AgentID, response.HostID, response.Lease = agentID, "host-"+agentID, c.lease()
	c.reply(msg, agentv1.KindRegisterResponse, response)
}

// provision creates the agent's durable pull consumer.
func (c *Controller) provision(agentID string) error {
	subject, err := agentv1.CommandSubject(agentID)
	if err != nil {
		return err
	}
	name, err := agentv1.CommandConsumer(agentID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = c.js.CreateOrUpdateConsumer(ctx, agentv1.CommandStream, jetstream.ConsumerConfig{
		Durable: name, FilterSubject: subject, AckPolicy: jetstream.AckExplicitPolicy,
		AckWait: 10 * time.Second, MaxDeliver: 20, DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	return err
}

func (c *Controller) onRenew(msg *nats.Msg) {
	var request agentv1.RenewRequest
	if err := agentv1.OpenFromAgent(agentv1.KindRenewRequest, msg.Data, agentv1.MaxControlMessageBytes, c.keyFor, &request); err != nil {
		return
	}
	response := &agentv1.RenewResponse{ProtocolVersion: agentv1.ProtocolVersion, RequestID: request.RequestID, AgentID: request.AgentID}
	if c.RevokeRenewals.Load() {
		response.Error = &agentv1.Error{Code: agentv1.ErrorLeaseRevoked}
	} else {
		response.Lease = c.lease()
	}
	c.reply(msg, agentv1.KindRenewResponse, response)
}

func (c *Controller) onResult(msg *nats.Msg) {
	var result agentv1.Result
	if err := agentv1.OpenFromAgent(agentv1.KindResult, msg.Data, agentv1.MaxResultMessageBytes, c.keyFor, &result); err != nil {
		return
	}
	// The subject must match the signer so one agent cannot report for another.
	if expected, err := agentv1.ResultSubject(result.AgentID); err != nil || msg.Subject != expected {
		return
	}
	select {
	case c.results <- result:
	default:
	}
	if result.Status.Terminal() && c.AutoAck.Load() {
		c.report(c.Send(context.Background(), result.AgentID, agentv1.Command{Type: agentv1.CommandResultAck, OperationID: result.OperationID}))
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

// Send fills in envelope fields, signs and publishes cmd with JetStream
// de-duplication on the command ID.
func (c *Controller) Send(ctx context.Context, agentID string, cmd agentv1.Command) error {
	return c.SendSignedBy(ctx, c.Key, agentID, cmd)
}

// SendSignedBy is Send with an arbitrary key, for forgery tests.
func (c *Controller) SendSignedBy(ctx context.Context, key ed25519.PrivateKey, agentID string, cmd agentv1.Command) error {
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
	subject, err := agentv1.CommandSubject(agentID)
	if err != nil {
		return err
	}
	_, err = c.js.Publish(ctx, subject, payload, jetstream.WithMsgID(cmd.CommandID))
	return err
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
