// Package app wires the agent together: the NATS connection, registration
// and lease renewal with the controller, the command consumer, and the
// operation dispatcher.
package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/machinarchy/machinarchy-agent/internal/capabilities"
	"github.com/machinarchy/machinarchy-agent/internal/config"
	"github.com/machinarchy/machinarchy-agent/internal/dispatch"
	"github.com/machinarchy/machinarchy-agent/internal/metrics"
	"github.com/machinarchy/machinarchy-agent/internal/state"
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
	conn     atomic.Pointer[nats.Conn]
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
	conn, err := a.connectWithStoredCredential()
	if err != nil {
		return err
	}
	a.conn.Store(conn.nc)
	defer func() { a.conn.Store(nil); a.disconnect(conn.nc) }()

	identity, err := a.ensureRegistered(ctx, conn.nc)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	// In operator mode the controller returns a NATS user JWT for this agent.
	// Switch to it, so the broker limits this connection to the agent's own
	// subjects, consumer and inbox.
	if identity.NATSUserJWT != "" && conn.agentID != identity.AgentID {
		next, err := a.connectAsAgent(identity.AgentID)
		if err != nil {
			return err
		}
		a.conn.Store(next.nc)
		a.disconnect(conn.nc)
		conn = next
		a.logger.Info("connected to NATS with the agent's own credential", "agent_id", identity.AgentID)
	}
	nc := conn.nc
	a.leaseExpiry.Store(identity.LeaseExpiresAt.UnixNano())
	a.logger.Info("agent registered", "agent_id", identity.AgentID, "host_id", identity.HostID,
		"capabilities", strings.Join(a.registry.Names(), ","))

	publisher := &resultPublisher{nc: nc, key: a.key}
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
	start("lease", func(ctx context.Context) error { return a.keepLease(ctx, nc, identity) })
	start("commands", func(ctx context.Context) error {
		// Pull commands only after recovery, so none is nak'ed for arriving
		// while the dispatcher is still starting.
		select {
		case <-disp.Ready():
		case <-ctx.Done():
			return nil
		}
		return a.consume(ctx, nc, identity.AgentID, disp)
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

func closed(nc *nats.Conn, timeout time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(timeout)
		for !nc.IsClosed() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		nc.Close()
	}()
	return done
}

// connection is a NATS connection and, when it authenticates with the agent's
// NATS user JWT, the agent ID that JWT belongs to.
type connection struct {
	nc      *nats.Conn
	agentID string
}

func (a *Agent) disconnect(nc *nats.Conn) {
	if err := nc.Drain(); err != nil && !errors.Is(err, nats.ErrConnectionClosed) {
		a.logger.Warn("NATS drain failed", "error", err)
		nc.Close()
	}
	<-closed(nc, a.cfg.NATS.DrainTimeout+time.Second)
}

// jwtRenewalMargin is how long before expiry a stored NATS user JWT is no
// longer used to start a connection.
const jwtRenewalMargin = 30 * time.Second

// connectWithStoredCredential uses the stored NATS user JWT while it is
// valid, and the configured bootstrap credential otherwise (first start, or
// an agent that was down longer than the JWT lifetime).
func (a *Agent) connectWithStoredCredential() (connection, error) {
	if identity := a.store.Snapshot().Identity; identity != nil && identity.NATSUserJWT != "" {
		if expires, err := jwtExpiry(identity.NATSUserJWT); err == nil && (expires.IsZero() || time.Now().Add(jwtRenewalMargin).Before(expires)) {
			return a.connectAsAgent(identity.AgentID)
		}
		a.logger.Info("stored NATS credential has expired; connecting with the bootstrap credential to renew it")
	}
	nc, err := a.connect(a.bootstrapAuth()...)
	return connection{nc: nc}, err
}

// connectAsAgent authenticates as the agent's own NATS user: the latest JWT
// from the state file, and the server nonce signed with the identity key.
func (a *Agent) connectAsAgent(agentID string) (connection, error) {
	inbox, err := agentv1.AgentInboxPrefix(agentID)
	if err != nil {
		return connection{}, err
	}
	credential := func() (string, error) {
		identity := a.store.Snapshot().Identity
		if identity == nil || identity.NATSUserJWT == "" {
			return "", errors.New("no NATS user JWT in agent state")
		}
		return identity.NATSUserJWT, nil
	}
	sign := func(nonce []byte) ([]byte, error) { return ed25519.Sign(a.key, nonce), nil }
	nc, err := a.connect(nats.UserJWT(credential, sign), nats.CustomInboxPrefix(inbox))
	return connection{nc: nc, agentID: agentID}, err
}

// bootstrapAuth is the configured credential used before the agent has a
// NATS user JWT.
func (a *Agent) bootstrapAuth() []nats.Option {
	c := a.cfg.NATS
	switch {
	case c.CredentialsFile != "":
		return []nats.Option{nats.UserCredentials(c.CredentialsFile)}
	case c.Token != "":
		return []nats.Option{nats.Token(c.Token)}
	case c.Username != "":
		return []nats.Option{nats.UserInfo(c.Username, c.Password)}
	}
	return nil
}

// jwtExpiry reads the "exp" claim of a JWT without verifying it; the NATS
// server verifies the credential. A zero time means no expiry.
func jwtExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("malformed JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("decode JWT claims: %w", err)
	}
	var claims struct {
		Expires int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("decode JWT claims: %w", err)
	}
	if claims.Expires == 0 {
		return time.Time{}, nil
	}
	return time.Unix(claims.Expires, 0), nil
}

func (a *Agent) connect(auth ...nats.Option) (*nats.Conn, error) {
	c := a.cfg.NATS
	options := []nats.Option{
		nats.Name(c.ClientName),
		nats.Timeout(c.ConnectTimeout),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(c.ReconnectWait),
		nats.ReconnectJitter(c.ReconnectWait/2, c.ReconnectWait/2),
		nats.RetryOnFailedConnect(true),
		nats.DrainTimeout(c.DrainTimeout),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			// A nil error is a close the agent asked for (e.g. leaving the
			// bootstrap connection): not a lost connection.
			if err != nil {
				a.recorder.NATSDisconnected()
				a.logger.Warn("disconnected from NATS; reconnecting", "error", err)
			}
		}),
		nats.ReconnectHandler(func(*nats.Conn) {
			a.recorder.NATSReconnected()
			a.logger.Info("reconnected to NATS")
		}),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			if errors.Is(err, nats.ErrAuthExpired) {
				// Expected once per NATS user JWT lifetime: the server closes
				// the connection and the client reconnects with the renewed JWT.
				a.logger.Info("NATS credential expired; reconnecting with the renewed one")
				return
			}
			a.logger.Warn("asynchronous NATS error", "error", err)
		}),
	}
	options = append(options, auth...)
	if c.UsesTLS() {
		options = append(options, nats.Secure(&tls.Config{MinVersion: tls.VersionTLS12}))
		if c.CAFile != "" {
			options = append(options, nats.RootCAs(c.CAFile))
		}
		if c.CertFile != "" {
			options = append(options, nats.ClientCert(c.CertFile, c.KeyFile))
		}
	}
	nc, err := nats.Connect(strings.Join(c.URLs, ","), options...)
	if err != nil {
		return nil, fmt.Errorf("configure NATS connection: %w", err)
	}
	return nc, nil
}

// ensureRegistered returns the stored identity or enrolls with the controller.
// Transient failures are retried with backoff until ctx ends.
func (a *Agent) ensureRegistered(ctx context.Context, nc *nats.Conn) (state.Identity, error) {
	if identity := a.store.Snapshot().Identity; identity != nil {
		// Confirm the identity is still valid before consuming commands.
		renewed, err := a.renewWithRetry(ctx, nc, *identity)
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
	return retry(ctx, a.logger, "registration", func() (state.Identity, error) { return a.register(ctx, nc, requestID) })
}

func (a *Agent) register(ctx context.Context, nc *nats.Conn, requestID string) (state.Identity, error) {
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
	if err := a.roundTrip(ctx, nc, agentv1.SubjectRegister, agentv1.KindRegisterRequest, request, agentv1.KindRegisterResponse, &response); err != nil {
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
		LeaseExpiresAt: response.Lease.ExpiresAt, HeartbeatIntervalMillis: response.Lease.HeartbeatIntervalMillis,
		NATSUserJWT: response.NATSUserJWT}
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

func (a *Agent) renewWithRetry(ctx context.Context, nc *nats.Conn, identity state.Identity) (state.Identity, error) {
	return retry(ctx, a.logger, "lease renewal", func() (state.Identity, error) { return a.renew(ctx, nc, identity) })
}

func (a *Agent) renew(ctx context.Context, nc *nats.Conn, identity state.Identity) (state.Identity, error) {
	requestID := newID()
	request := &agentv1.RenewRequest{ProtocolVersion: agentv1.ProtocolVersion, RequestID: requestID, AgentID: identity.AgentID,
		LeaseID: identity.LeaseID, AgentVersion: truncate(a.version, 64), SentAt: time.Now().UTC()}
	var response agentv1.RenewResponse
	if err := a.roundTrip(ctx, nc, agentv1.SubjectLeaseRenew, agentv1.KindRenewRequest, request, agentv1.KindRenewResponse, &response); err != nil {
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
	if response.NATSUserJWT != "" {
		identity.NATSUserJWT = response.NATSUserJWT
	}
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
	if nc := a.conn.Load(); nc != nil {
		g.NATSConnected = nc.IsConnected()
	}
	if disp := a.disp.Load(); disp != nil {
		g.QueueDepth, g.Running = disp.Stats()
	}
	return g
}

// keepLease renews on the controller-requested cadence. Commands are paused
// whenever the lease has expired (see consume).
func (a *Agent) keepLease(ctx context.Context, nc *nats.Conn, identity state.Identity) error {
	for {
		timer := time.NewTimer(identity.HeartbeatInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		renewed, err := a.renewWithRetry(ctx, nc, identity)
		if err != nil {
			return err
		}
		identity = renewed
	}
}

func (a *Agent) leaseValid() bool {
	return time.Now().UnixNano() < a.leaseExpiry.Load()
}

func (a *Agent) roundTrip(ctx context.Context, nc *nats.Conn, subject string, requestKind agentv1.Kind, request agentv1.Body, responseKind agentv1.Kind, response agentv1.Body) error {
	payload, err := agentv1.Seal(a.key, requestKind, request)
	if err != nil {
		return permanent(err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, a.cfg.RequestTimeout)
	defer cancel()
	msg, err := nc.RequestWithContext(requestCtx, subject, payload)
	if err != nil {
		return fmt.Errorf("%s request: %w", requestKind, err)
	}
	if err := agentv1.OpenAny(a.cfg.ControllerPublicKeys, responseKind, msg.Data, agentv1.MaxControlMessageBytes, response); err != nil {
		// A response that is not signed by the pinned controller key is
		// treated as no response at all; keep waiting for the real one.
		return fmt.Errorf("%s: %w", responseKind, err)
	}
	return nil
}

// consume binds to the controller-provisioned durable consumer and hands
// verified commands to the dispatcher.
func (a *Agent) consume(ctx context.Context, nc *nats.Conn, agentID string, disp *dispatch.Dispatcher) error {
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("JetStream: %w", err)
	}
	subject, err := agentv1.CommandSubject(agentID)
	if err != nil {
		return err
	}
	name, err := agentv1.CommandConsumer(agentID)
	if err != nil {
		return err
	}
	consumer, err := retry(ctx, a.logger, "command consumer binding", func() (jetstream.Consumer, error) {
		bindCtx, cancel := context.WithTimeout(ctx, a.cfg.RequestTimeout)
		defer cancel()
		consumer, err := js.Consumer(bindCtx, agentv1.CommandStream, name)
		if err != nil {
			return nil, fmt.Errorf("bind consumer %s: %w", name, err)
		}
		info := consumer.CachedInfo()
		if info.Config.FilterSubject != subject || info.Config.AckPolicy != jetstream.AckExplicitPolicy {
			return nil, permanent(fmt.Errorf("consumer %s must filter %s with explicit acks", name, subject))
		}
		return consumer, nil
	})
	if err != nil {
		return err
	}
	messages, err := consumer.Messages(jetstream.PullMaxMessages(a.cfg.QueueSize))
	if err != nil {
		return fmt.Errorf("start command consumer: %w", err)
	}
	stop := context.AfterFunc(ctx, messages.Stop)
	defer stop()
	defer messages.Stop()
	a.logger.Info("consuming commands", "consumer", name)
	for {
		msg, err := messages.Next()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				return nil
			}
			a.logger.Warn("command iterator error", "error", err)
			continue
		}
		a.handle(ctx, agentID, disp, msg)
	}
}

func (a *Agent) handle(ctx context.Context, agentID string, disp *dispatch.Dispatcher, msg jetstream.Msg) {
	if !a.leaseValid() {
		a.settle(msg.NakWithDelay(5*time.Second), "nak (lease expired)") // paused until the lease is renewed
		return
	}
	var cmd agentv1.Command
	if err := agentv1.OpenAny(a.cfg.ControllerPublicKeys, agentv1.KindCommand, msg.Data(), agentv1.MaxCommandMessageBytes, &cmd); err != nil {
		a.logger.Warn("discarding unauthenticated or malformed command", "error", err)
		a.settle(msg.Term(), "term")
		return
	}
	if err := cmd.ValidateAt(agentID, time.Now()); err != nil {
		a.logger.Warn("discarding command", "command_id", cmd.CommandID, "error", err)
		a.settle(msg.Term(), "term")
		return
	}
	if err := disp.Handle(ctx, cmd); err != nil {
		a.logger.Warn("command not processed; will be redelivered", "command_id", cmd.CommandID, "error", err)
		a.settle(msg.NakWithDelay(2*time.Second), "nak")
		return
	}
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.cfg.RequestTimeout)
	defer cancel()
	if err := msg.DoubleAck(ackCtx); err != nil {
		// Redelivery is harmless: the replay ledger drops duplicates.
		a.logger.Warn("command acknowledgement not confirmed", "command_id", cmd.CommandID, "error", err)
	}
}

// settle logs a failed nak/term; JetStream redelivers after AckWait anyway.
func (a *Agent) settle(err error, action string) {
	if err != nil {
		a.logger.Warn("command "+action+" failed; broker will redeliver after ack wait", "error", err)
	}
}

type resultPublisher struct {
	nc  *nats.Conn
	key ed25519.PrivateKey
}

func (p *resultPublisher) PublishResult(_ context.Context, result agentv1.Result) error {
	payload, err := agentv1.Seal(p.key, agentv1.KindResult, &result)
	if err != nil {
		return err
	}
	if len(payload) > agentv1.MaxResultMessageBytes {
		return errors.New("result exceeds the maximum message size")
	}
	if !p.nc.IsConnected() {
		return errors.New("not connected to NATS")
	}
	subject, err := agentv1.ResultSubject(result.AgentID)
	if err != nil {
		return err
	}
	return p.nc.Publish(subject, payload)
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
