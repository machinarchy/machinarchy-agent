// Package app wires the agent together: the gateway connection, registration
// and lease renewal with the controller, the command stream, and the
// operation dispatcher.
package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/capabilities"
	"github.com/machinarchy/machinarchy-agent/internal/config"
	"github.com/machinarchy/machinarchy-agent/internal/dispatch"
	"github.com/machinarchy/machinarchy-agent/internal/metrics"
	"github.com/machinarchy/machinarchy-agent/internal/state"
	"github.com/machinarchy/machinarchy-agent/internal/transport"
	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// ErrReenrollmentRequired means the controller no longer recognises this
// agent. The stored identity is cleared; a new enrollment token is needed.
var ErrReenrollmentRequired = errors.New("controller rejected this agent's identity; configure a new enrollment token to re-enroll")

// ErrEnrollmentRejected means the controller refused to enrol this agent: the
// token is wrong, or this key was revoked. Retrying cannot succeed.
var ErrEnrollmentRejected = errors.New("controller rejected enrollment; check the enrollment token, or re-enrol a revoked host with a new key")

// NeedsOperator reports whether err ends the agent until an operator acts
// (new token, new key). The process exit code tells supervisors not to
// restart it.
func NeedsOperator(err error) bool {
	return errors.Is(err, ErrReenrollmentRequired) || errors.Is(err, ErrEnrollmentRejected)
}

// Agent is one agent process.
type Agent struct {
	cfg      config.Config
	version  string
	logger   *slog.Logger
	store    *state.Store
	registry *capabilities.Registry
	key      ed25519.PrivateKey

	leaseExpiry atomic.Int64 // unix nanoseconds

	// For the metrics textfile (ADR-0004).
	recorder *metrics.Recorder
	client   atomic.Pointer[transport.Client]
	disp     atomic.Pointer[dispatch.Dispatcher]
}

// New opens local state and builds the capability registry. recorder
// receives connection and lease events; nil keeps a private one.
func New(cfg config.Config, version string, logger *slog.Logger, recorder *metrics.Recorder) (*Agent, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if recorder == nil {
		recorder = &metrics.Recorder{}
	}
	store, err := state.Open(cfg.StateFile)
	if err != nil {
		return nil, fmt.Errorf("open agent state: %w", err)
	}
	registry, err := capabilities.New(capabilities.Options{AgentVersion: version, ServiceEnabled: cfg.ServiceEnabled, Services: cfg.Services})
	if err != nil {
		return nil, fmt.Errorf("initialize capabilities: %w", err)
	}
	return &Agent{cfg: cfg, version: version, logger: logger, store: store, registry: registry, key: store.PrivateKey(), recorder: recorder}, nil
}

// PublicKey is the agent's identity key (sent at registration).
func (a *Agent) PublicKey() ed25519.PublicKey { return a.key.Public().(ed25519.PublicKey) }

// Run connects, registers if needed and serves commands until ctx is
// cancelled. It returns nil on a clean shutdown.
func (a *Agent) Run(ctx context.Context) error {
	if a.cfg.MetricsFile != "" {
		metricsCtx, stopMetrics := context.WithCancel(context.WithoutCancel(ctx))
		writer := &metrics.Writer{Path: a.cfg.MetricsFile, Interval: a.cfg.MetricsInterval, Recorder: a.recorder,
			Collect: a.gauges, Logger: a.logger.With("component", "metrics")}
		done := make(chan struct{})
		go func() { defer close(done); writer.Run(metricsCtx) }()
		// Runs last: the final write shows the agent disconnected.
		defer func() { stopMetrics(); <-done }()
	}
	client, err := transport.Dial(transport.Options{
		ServerURL: a.cfg.Server.URL, CAFile: a.cfg.Server.CAFile, AllowPlaintext: a.cfg.Environment == config.Development,
		UserAgent: "machinarchy-agent/" + truncate(a.version, 32),
		OnDisconnect: func() {
			a.recorder.ControllerDisconnected()
			a.logger.Warn("disconnected from the controller; reconnecting")
		},
		OnReconnect: func() { a.recorder.ControllerReconnected(); a.logger.Info("reconnected to the controller") },
	})
	if err != nil {
		return err
	}
	a.client.Store(&client)
	defer func() {
		a.client.Store(nil)
		if err := client.Close(); err != nil {
			a.logger.Warn("closing the controller connection failed", "error", err)
		}
	}()

	identity, err := a.ensureRegistered(ctx, client)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	a.leaseExpiry.Store(identity.LeaseExpiresAt.UnixNano())
	a.logger.Info("agent registered", "agent_id", identity.AgentID, "host_id", identity.HostID,
		"capabilities", strings.Join(a.registry.Names(), ","))

	publisher := &resultPublisher{client: client, key: a.key, timeout: a.cfg.RequestTimeout}
	disp, err := dispatch.New(a.store, a.registry, publisher, dispatch.Options{
		AgentID: identity.AgentID, Workers: a.cfg.Workers, QueueSize: a.cfg.QueueSize,
		MaxOperationTimeout: a.cfg.MaxOperationTimeout, MaxOutputBytes: a.cfg.MaxOutputBytes,
		ShutdownTimeout: a.cfg.ShutdownTimeout,
	}, a.logger)
	if err != nil {
		return err
	}
	a.disp.Store(disp)
	defer a.disp.Store(nil)

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	start := func(name string, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(runCtx); err != nil && runCtx.Err() == nil {
				a.logger.Error("agent component stopped", "component", name, "error", err)
				cancel(err)
			}
		}()
	}
	// The dispatcher outlives the consumer so in-flight work can finish.
	dispCtx, stopDispatch := context.WithCancel(context.WithoutCancel(ctx))
	dispDone := make(chan error, 1)
	go func() { dispDone <- disp.Run(dispCtx) }()
	start("lease", func(ctx context.Context) error { return a.keepLease(ctx, client, identity) })
	start("commands", func(ctx context.Context) error {
		// Take commands only after recovery, so none is refused for arriving
		// while the dispatcher is still starting.
		select {
		case <-disp.Ready():
		case <-ctx.Done():
			return nil
		}
		return a.consume(ctx, client, disp)
	})

	<-runCtx.Done()
	wg.Wait()
	stopDispatch()
	dispErr := <-dispDone
	if cause := context.Cause(runCtx); cause != nil && !errors.Is(cause, context.Canceled) {
		return errors.Join(cause, dispErr)
	}
	return dispErr
}

// ensureRegistered returns the stored identity or enrolls with the controller.
// Transient failures are retried with backoff until ctx ends.
func (a *Agent) ensureRegistered(ctx context.Context, client transport.Client) (state.Identity, error) {
	if identity := a.store.Snapshot().Identity; identity != nil {
		// Confirm the identity is still valid before consuming commands.
		renewed, err := a.renewWithRetry(ctx, client, *identity)
		if err == nil {
			return renewed, nil
		}
		if !errors.Is(err, ErrReenrollmentRequired) || a.cfg.EnrollmentToken == "" {
			return state.Identity{}, err
		}
		a.logger.Warn("stored identity rejected; re-enrolling with the configured token")
	}
	if a.cfg.EnrollmentToken == "" {
		return state.Identity{}, errors.New("agent is not registered and no enrollment token is configured")
	}
	requestID := a.store.Snapshot().PendingRegistrationID
	if requestID == "" {
		requestID = newID()
		if err := a.store.Update(func(doc *state.Document) error { doc.PendingRegistrationID = requestID; return nil }); err != nil {
			return state.Identity{}, err
		}
	}
	return retry(ctx, a.logger, "registration", func() (state.Identity, error) { return a.register(ctx, client, requestID) })
}

func (a *Agent) register(ctx context.Context, client transport.Client, requestID string) (state.Identity, error) {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown"
	}
	request := &agentv1.RegisterRequest{
		ProtocolVersion: agentv1.ProtocolVersion, RequestID: requestID, EnrollmentToken: a.cfg.EnrollmentToken,
		AgentPublicKey: a.PublicKey(), Hostname: truncate(hostname, 255), AgentVersion: truncate(a.version, 64),
		OS: runtime.GOOS, Architecture: runtime.GOARCH, Capabilities: a.registry.Names(), SentAt: time.Now().UTC(),
	}
	var response agentv1.RegisterResponse
	if err := a.roundTrip(ctx, client.Register, agentv1.KindRegisterRequest, request, agentv1.KindRegisterResponse, &response); err != nil {
		return state.Identity{}, err
	}
	if response.RequestID != requestID {
		return state.Identity{}, errors.New("registration response does not match the request")
	}
	if response.Error != nil {
		if response.Error.Code == agentv1.ErrorEnrollmentRejected {
			return state.Identity{}, permanent(fmt.Errorf("%w (%w)", ErrEnrollmentRejected, response.Error))
		}
		if !response.Error.Retryable {
			return state.Identity{}, permanent(response.Error)
		}
		return state.Identity{}, response.Error
	}
	identity := state.Identity{AgentID: response.AgentID, HostID: response.HostID, LeaseID: response.Lease.LeaseID,
		LeaseExpiresAt: response.Lease.ExpiresAt, HeartbeatIntervalMillis: response.Lease.HeartbeatIntervalMillis}
	if err := a.store.Update(func(doc *state.Document) error {
		doc.Identity = &identity
		doc.PendingRegistrationID = ""
		return nil
	}); err != nil {
		return state.Identity{}, permanent(fmt.Errorf("persist identity: %w", err))
	}
	a.recorder.LeaseRenewed(time.Now())
	return identity, nil
}

func (a *Agent) renewWithRetry(ctx context.Context, client transport.Client, identity state.Identity) (state.Identity, error) {
	return retry(ctx, a.logger, "lease renewal", func() (state.Identity, error) { return a.renew(ctx, client, identity) })
}

func (a *Agent) renew(ctx context.Context, client transport.Client, identity state.Identity) (state.Identity, error) {
	requestID := newID()
	request := &agentv1.RenewRequest{ProtocolVersion: agentv1.ProtocolVersion, RequestID: requestID, AgentID: identity.AgentID,
		LeaseID: identity.LeaseID, AgentVersion: truncate(a.version, 64), SentAt: time.Now().UTC()}
	var response agentv1.RenewResponse
	if err := a.roundTrip(ctx, client.Renew, agentv1.KindRenewRequest, request, agentv1.KindRenewResponse, &response); err != nil {
		return state.Identity{}, err
	}
	if response.RequestID != requestID || response.AgentID != identity.AgentID {
		return state.Identity{}, errors.New("lease response does not match the request")
	}
	if response.Error != nil {
		switch response.Error.Code {
		case agentv1.ErrorUnknownAgent, agentv1.ErrorLeaseRevoked:
			if err := a.store.Update(func(doc *state.Document) error { doc.Identity = nil; return nil }); err != nil {
				return state.Identity{}, permanent(err)
			}
			return state.Identity{}, permanent(ErrReenrollmentRequired)
		}
		if !response.Error.Retryable {
			return state.Identity{}, permanent(response.Error)
		}
		return state.Identity{}, response.Error
	}
	identity.LeaseID = response.Lease.LeaseID
	identity.LeaseExpiresAt = response.Lease.ExpiresAt
	identity.HeartbeatIntervalMillis = response.Lease.HeartbeatIntervalMillis
	if err := a.store.Update(func(doc *state.Document) error { doc.Identity = &identity; return nil }); err != nil {
		return state.Identity{}, permanent(fmt.Errorf("persist lease: %w", err))
	}
	a.leaseExpiry.Store(identity.LeaseExpiresAt.UnixNano())
	a.recorder.LeaseRenewed(time.Now())
	return identity, nil
}

// gauges reads the agent's current state for the metrics textfile.
func (a *Agent) gauges() metrics.Gauges {
	counts := a.store.Counts()
	g := metrics.Gauges{Version: a.version, Enrolled: counts.Enrolled, LeaseExpiresAt: counts.LeaseExpiresAt,
		Operations: make(map[metrics.OperationKey]int, len(counts.Operations)), ResultsAwaitingAck: counts.ResultsAwaitingAck,
		QueueSize: a.cfg.QueueSize}
	for key, n := range counts.Operations {
		g.Operations[metrics.OperationKey{Phase: key[0], Status: key[1]}] = n
	}
	if client := a.client.Load(); client != nil {
		g.ControllerConnected = (*client).Connected()
	}
	if disp := a.disp.Load(); disp != nil {
		g.QueueDepth, g.Running = disp.Stats()
	}
	return g
}

// keepLease renews on the controller-requested cadence. Commands are paused
// whenever the lease has expired (see consume).
func (a *Agent) keepLease(ctx context.Context, client transport.Client, identity state.Identity) error {
	for {
		timer := time.NewTimer(identity.HeartbeatInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		renewed, err := a.renewWithRetry(ctx, client, identity)
		if err != nil {
			return err
		}
		identity = renewed
	}
}

func (a *Agent) leaseValid() bool {
	return time.Now().UnixNano() < a.leaseExpiry.Load()
}

func (a *Agent) roundTrip(ctx context.Context, call func(context.Context, []byte) ([]byte, error), requestKind agentv1.Kind, request agentv1.Body, responseKind agentv1.Kind, response agentv1.Body) error {
	payload, err := agentv1.Seal(a.key, requestKind, request)
	if err != nil {
		return permanent(err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, a.cfg.RequestTimeout)
	defer cancel()
	data, err := call(requestCtx, payload)
	if err != nil {
		return fmt.Errorf("%s request: %w", requestKind, err)
	}
	if err := agentv1.OpenAny(a.cfg.ControllerPublicKeys, responseKind, data, agentv1.MaxControlMessageBytes, response); err != nil {
		// A response that is not signed by the pinned controller key is
		// never trusted; it is retried like a lost response.
		return fmt.Errorf("%s: %w", responseKind, err)
	}
	return nil
}

// streamBackoff bounds the wait between command stream attempts.
const (
	streamBackoffFloor   = time.Second
	streamBackoffCeiling = 30 * time.Second
	// streamStable is how long a stream must have run before its failure
	// resets the backoff.
	streamStable = 10 * time.Second
)

// consume keeps one command stream open and hands verified commands to the
// dispatcher. It reconnects with backoff after a lost connection, a restart of
// the controller or a stream the controller closed; a lease that is not valid
// yet is retried once the lease keeper has renewed it.
func (a *Agent) consume(ctx context.Context, client transport.Client, disp *dispatch.Dispatcher) error {
	delay := streamBackoffFloor
	for {
		began := time.Now()
		err := a.serveStream(ctx, client, disp)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(began) >= streamStable {
			delay = streamBackoffFloor
		}
		wait := delay/2 + mathrand.N(delay/2+1) // #nosec G404 -- retry jitter only
		a.logger.Warn("command stream ended; reopening", "error", err, "retry_in", wait.Round(time.Millisecond).String())
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		delay = min(delay*2, streamBackoffCeiling)
	}
}

// serveStream runs one command stream until it ends.
func (a *Agent) serveStream(ctx context.Context, client transport.Client, disp *dispatch.Dispatcher) error {
	identity := a.store.Snapshot().Identity
	if identity == nil {
		return errors.New("agent has no identity")
	}
	if !a.leaseValid() {
		return errors.New("lease has expired; waiting for a renewal")
	}
	request := &agentv1.RenewRequest{ProtocolVersion: agentv1.ProtocolVersion, RequestID: newID(), AgentID: identity.AgentID,
		LeaseID: identity.LeaseID, AgentVersion: truncate(a.version, 64), SentAt: time.Now().UTC()}
	open, err := agentv1.Seal(a.key, agentv1.KindRenewRequest, request)
	if err != nil {
		return err
	}
	stream, err := client.Commands(ctx, open)
	if err != nil {
		return err
	}
	defer stream.Close()
	a.logger.Info("consuming commands")
	for {
		data, err := stream.Recv()
		if err != nil {
			return err
		}
		if err := a.handle(ctx, identity.AgentID, disp, stream, data); err != nil {
			return err
		}
	}
}

// handle verifies one command and acknowledges it once the dispatcher has
// taken it over. Returning an error ends the stream without an acknowledgement,
// so the controller redelivers the command on the next stream.
func (a *Agent) handle(ctx context.Context, agentID string, disp *dispatch.Dispatcher, stream transport.CommandStream, data []byte) error {
	if !a.leaseValid() {
		return errors.New("lease expired; command left for redelivery") // paused until the lease is renewed
	}
	var cmd agentv1.Command
	if err := agentv1.OpenAny(a.cfg.ControllerPublicKeys, agentv1.KindCommand, data, agentv1.MaxCommandMessageBytes, &cmd); err != nil {
		a.logger.Warn("discarding unauthenticated or malformed command", "error", err)
		return stream.Ack("")
	}
	if err := cmd.ValidateAt(agentID, time.Now()); err != nil {
		a.logger.Warn("discarding command", "command_id", cmd.CommandID, "error", err)
		return stream.Ack(cmd.CommandID)
	}
	if err := disp.Handle(ctx, cmd); err != nil {
		a.logger.Warn("command not processed; will be redelivered", "command_id", cmd.CommandID, "error", err)
		return err
	}
	// A lost acknowledgement means redelivery, which is harmless: the replay
	// ledger drops duplicates.
	return stream.Ack(cmd.CommandID)
}

type resultPublisher struct {
	client  transport.Client
	key     ed25519.PrivateKey
	timeout time.Duration
}

func (p *resultPublisher) PublishResult(ctx context.Context, result agentv1.Result) error {
	payload, err := agentv1.Seal(p.key, agentv1.KindResult, &result)
	if err != nil {
		return err
	}
	if len(payload) > agentv1.MaxResultMessageBytes {
		return errors.New("result exceeds the maximum message size")
	}
	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	return p.client.SendResult(callCtx, payload)
}

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func permanent(err error) error { return permanentError{err: err} }

// retry calls fn with jittered exponential backoff (1s..60s) until it
// succeeds, returns a permanent error, or ctx ends.
func retry[T any](ctx context.Context, logger *slog.Logger, what string, fn func() (T, error)) (T, error) {
	delay := time.Second
	for {
		value, err := fn()
		if err == nil {
			return value, nil
		}
		var zero T
		var perm permanentError
		if errors.As(err, &perm) {
			return zero, perm.err
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		wait := delay/2 + mathrand.N(delay/2+1) // #nosec G404 -- retry jitter only
		logger.Warn(what+" failed; retrying", "error", err, "retry_in", wait.Round(time.Millisecond).String())
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, time.Minute)
	}
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err)) // unrecoverable platform failure
	}
	return hex.EncodeToString(b)
}

func truncate(value string, n int) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	if len(value) > n {
		return value[:n]
	}
	return value
}
