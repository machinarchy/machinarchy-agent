package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/capabilities"
	agentconfig "github.com/machinarchy/machinarchy-agent/internal/config"
	"github.com/machinarchy/machinarchy-agent/internal/controlplane"
	"github.com/machinarchy/machinarchy-agent/internal/events"
	agentidentity "github.com/machinarchy/machinarchy-agent/internal/identity"
	"github.com/machinarchy/machinarchy-agent/internal/operations"
	agenttransport "github.com/machinarchy/machinarchy-agent/internal/transport"
	agentv1 "github.com/machinarchy/machinarchy-agent/protocol/agent/v1"
)

// Session manages control-plane identity registration and lease heartbeats.
// It starts only after the event broker is connected.
type Session interface {
	Run(context.Context, agenttransport.Connection, <-chan agenttransport.ConnectionEvent) error
}

type AgentSession struct {
	config  agentconfig.Config
	logger  *slog.Logger
	store   agentidentity.IdentityStore
	opStore operations.OperationStore
	ledger  events.EventLedger
}

func NewAgentSession(cfg agentconfig.Config, logger *slog.Logger, store agentidentity.IdentityStore) (*AgentSession, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate agent session configuration: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if store == nil {
		return nil, errors.New("agent identity store is required")
	}
	ledger, err := events.NewFileEventLedger(cfg.IdentityFile + ".events.json")
	if err != nil {
		return nil, fmt.Errorf("initialize agent event ledger: %w", err)
	}
	operationStorePath := cfg.OperationStoreFile
	if operationStorePath == "" {
		operationStorePath = cfg.IdentityFile + ".operations.json"
	}
	opStore, err := operations.NewFileOperationStore(operationStorePath)
	if err != nil {
		return nil, fmt.Errorf("initialize durable operation store: %w", err)
	}
	sessionLogger := logger.With("component", "agent-session")
	return &AgentSession{
		config:  cfg,
		logger:  sessionLogger,
		store:   store,
		opStore: opStore,
		ledger:  ledger,
	}, nil
}

func (s *AgentSession) Run(ctx context.Context, conn agenttransport.Connection, connectionEvents <-chan agenttransport.ConnectionEvent) (runErr error) {
	if ctx == nil {
		return errors.New("agent session context is required")
	}
	requester, ok := conn.(controlplane.Requester)
	if !ok {
		return errors.New("agent broker connection does not support request/reply")
	}
	client, err := controlplane.NewNATSControlPlaneClient(requester, s.config)
	if err != nil {
		return err
	}

	identity, err := s.ensureRegisteredWithRetry(ctx, client)
	if err != nil {
		return err
	}
	s.logger.Info("agent identity established", "agent_id", identity.AgentID, "host_id", identity.HostID)

	// Renew the persisted lease before declaring the runtime active. renew()
	// refuses to send a request for an already expired lease; an expired identity
	// requires explicit re-enrollment or controller-side recovery.
	identity, err = s.renewWithRetry(ctx, client, identity)
	if err != nil {
		return fmt.Errorf("establish initial agent lease: %w", err)
	}

	subscriber, ok := conn.(events.EventSubscriber)
	if !ok {
		return errors.New("agent broker connection does not support event subscriptions")
	}
	subject, err := events.AgentEventsSubject(identity.AgentID)
	if err != nil {
		return fmt.Errorf("derive agent event subscription subject: %w", err)
	}
	consumerName, err := agentv1.AgentEventsConsumerName(identity.AgentID)
	if err != nil {
		return fmt.Errorf("derive agent event durable consumer name: %w", err)
	}
	subscription, err := subscriber.Subscribe(ctx, subject, consumerName, s.config.EventBufferSize)
	if err != nil {
		return fmt.Errorf("subscribe to agent events: %w", err)
	}
	if subscription == nil {
		return errors.New("agent broker returned a nil event subscription")
	}
	var dispatcher *operations.OperationDispatcher
	defer func() {
		// Stop inbound delivery before touching accepted work. A normal process
		// shutdown drains accepted operations; an unexpected session failure
		// aborts them because the agent may no longer hold valid authority.
		if closeErr := subscription.Close(); closeErr != nil {
			wrapped := fmt.Errorf("close agent event subscription: %w", closeErr)
			s.logger.Warn("close agent event subscription failed", "error", closeErr)
			runErr = errors.Join(runErr, wrapped)
		}
		if dispatcher != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), s.config.DrainTimeout)
			defer cleanupCancel()
			var cleanupErr error
			if runErr != nil && ctx.Err() == nil {
				cleanupErr = dispatcher.Abort(cleanupCtx)
			} else {
				cleanupErr = dispatcher.Shutdown(cleanupCtx)
			}
			if cleanupErr != nil {
				s.logger.Error("operation dispatcher cleanup did not complete cleanly", "error", cleanupErr)
				runErr = errors.Join(runErr, cleanupErr)
			}
		}
	}()
	messagePublisher, ok := conn.(operations.MessagePublisher)
	if !ok {
		return errors.New("agent broker connection does not support outbound message publishing")
	}
	resultPublisher, err := operations.NewNATSOperationResultPublisher(messagePublisher)
	if err != nil {
		return fmt.Errorf("initialize operation result publisher: %w", err)
	}
	registry, err := capabilities.NewDefaultRegistry(s.config.AgentVersion, s.config)
	if err != nil {
		return fmt.Errorf("initialize agent capability registry: %w", err)
	}
	dispatcher, err = operations.NewOperationDispatcherWithStore(identity, registry, resultPublisher, s.opStore, operations.OperationDispatcherConfig{
		Workers:              s.config.OperationWorkers,
		QueueSize:            s.config.OperationQueueSize,
		MaxOperationTimeout:  s.config.MaxOperationTimeout,
		MaxInputBytes:        s.config.MaxOperationInputBytes,
		MaxOutputBytes:       s.config.MaxOperationOutputBytes,
		ResultPublishTimeout: s.config.ResultPublishTimeout,
	}, s.logger)
	if err != nil {
		return fmt.Errorf("initialize operation dispatcher: %w", err)
	}
	if err := dispatcher.Start(ctx); err != nil {
		return fmt.Errorf("start operation dispatcher: %w", err)
	}
	processor, err := events.NewAgentEventProcessor(identity, s.ledger, dispatcher, s.logger)
	if err != nil {
		return fmt.Errorf("initialize agent event processor: %w", err)
	}
	s.logger.Info("subscribed to agent events", "agent_id", identity.AgentID, "subject", subject)

	messageStream := subscription.Messages()
	var pendingMessage events.EventMessage
	processMessage := func(message events.EventMessage) error {
		if message == nil || len(message.Payload()) == 0 {
			return nil
		}
		processingErr := processor.Handle(ctx, message.Payload())
		ackCtx, ackCancel := context.WithTimeout(context.Background(), s.config.RequestTimeout)
		defer ackCancel()
		switch {
		case processingErr == nil || errors.Is(processingErr, events.ErrDuplicateAgentEvent):
			if errors.Is(processingErr, events.ErrDuplicateAgentEvent) {
				s.logger.Debug("duplicate agent event ignored")
			}
			if ackErr := message.Ack(ackCtx); ackErr != nil {
				// The local ledger makes redelivery safe. Leave the message pending
				// when the broker cannot confirm the acknowledgement.
				s.logger.Warn("could not confirm durable event acknowledgement; broker may redeliver", "error", ackErr)
			}
		case isInvalidAgentEvent(processingErr):
			var invalidEvent *events.InvalidAgentEventError
			_ = errors.As(processingErr, &invalidEvent)
			s.logger.Warn("terminating invalid agent event", "reason", invalidEvent.Error())
			if termErr := message.Term(ackCtx); termErr != nil {
				s.logger.Warn("could not terminate invalid durable event", "error", termErr)
			}
		default:
			// Storage failures and transient handler errors must not be acknowledged.
			// Delayed NAK avoids a hot redelivery loop while preserving the message.
			if nakErr := message.Nak(ackCtx, s.config.EventNakDelay); nakErr != nil {
				s.logger.Warn("could not negatively acknowledge durable event; broker will retry after AckWait", "error", nakErr)
			}
		}
		return nil
	}
	resumeMessageDelivery := func() error {
		if !conn.IsConnected() {
			return nil
		}
		if err := agentidentity.EnsureLeaseAlive(identity); err != nil {
			return err
		}
		if pendingMessage != nil {
			message := pendingMessage
			pendingMessage = nil
			if err := processMessage(message); err != nil {
				return err
			}
		}
		messageStream = subscription.Messages()
		return nil
	}

	retryDelay := 2 * time.Second
	timer := time.NewTimer(identity.HeartbeatInterval())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("agent session shutdown requested", "agent_id", identity.AgentID)
			return nil
		case message, open := <-messageStream:
			if !open {
				return errors.New("agent event subscription message stream closed")
			}
			// NATS may still have locally buffered messages when the connection
			// drops. Hold at most the message already selected by this loop and
			// stop consuming until connectivity and the lease are re-established.
			if !conn.IsConnected() {
				pendingMessage = message
				messageStream = nil
				continue
			}
			if err := processMessage(message); err != nil {
				return err
			}
		case event, open := <-connectionEvents:
			if !open {
				return errors.New("agent broker lifecycle event stream closed")
			}
			switch event.Type {
			case agenttransport.EventDisconnected:
				messageStream = nil
				if event.Err != nil {
					s.logger.Warn("broker disconnected; agent lease will be renewed after connectivity returns", "agent_id", identity.AgentID, "error", event.Err)
				} else {
					s.logger.Warn("broker disconnected; agent lease will be renewed after connectivity returns", "agent_id", identity.AgentID)
				}
			case agenttransport.EventReconnected:
				s.logger.Info("broker reconnected; renewing agent lease", "agent_id", identity.AgentID)
				updated, renewErr := s.renew(ctx, client, identity)
				if renewErr != nil {
					if ctx.Err() != nil {
						return nil
					}
					if !isRetryable(renewErr) {
						return renewErr
					}
					deadline, deadlineErr := retryDelayBeforeExpiry(retryDelay, identity.HeartbeatInterval(), identity.LeaseExpiresAt)
					if deadlineErr != nil {
						return deadlineErr
					}
					s.logger.Warn("agent lease renewal failed after reconnect; retrying", "agent_id", identity.AgentID, "error", renewErr)
					retryDelay = deadline
					resetTimer(timer, retryDelay)
					continue
				}
				identity = updated
				if updateErr := processor.UpdateIdentity(identity); updateErr != nil {
					return fmt.Errorf("update agent event processor identity after lease renewal: %w", updateErr)
				}
				if resumeErr := resumeMessageDelivery(); resumeErr != nil {
					return fmt.Errorf("resume agent event delivery after reconnect: %w", resumeErr)
				}
				retryDelay = 2 * time.Second
				resetTimer(timer, identity.HeartbeatInterval())
			case agenttransport.EventAsyncError:
				if event.Err != nil {
					s.logger.Warn("asynchronous broker error", "error", event.Err)
				}
			case agenttransport.EventClosed:
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("event broker connection closed during agent session")
			}
		case <-timer.C:
			updated, renewErr := s.renew(ctx, client, identity)
			if renewErr != nil {
				if ctx.Err() != nil {
					return nil
				}
				if !isRetryable(renewErr) {
					return renewErr
				}
				deadline, deadlineErr := retryDelayBeforeExpiry(retryDelay, identity.HeartbeatInterval(), identity.LeaseExpiresAt)
				if deadlineErr != nil {
					return deadlineErr
				}
				s.logger.Warn("agent lease renewal failed; retrying before expiration", "agent_id", identity.AgentID, "error", renewErr)
				retryDelay = deadline
				resetTimer(timer, retryDelay)
				continue
			}
			identity = updated
			if updateErr := processor.UpdateIdentity(identity); updateErr != nil {
				return fmt.Errorf("update agent event processor identity after heartbeat: %w", updateErr)
			}
			if resumeErr := resumeMessageDelivery(); resumeErr != nil {
				return fmt.Errorf("resume agent event delivery after heartbeat: %w", resumeErr)
			}
			retryDelay = 2 * time.Second
			resetTimer(timer, identity.HeartbeatInterval())
		}
	}
}

func (s *AgentSession) ensureRegisteredWithRetry(ctx context.Context, client controlplane.ControlPlane) (agentidentity.AgentIdentity, error) {
	deadline := time.Now().Add(s.config.StartupTimeout)
	delay := 2 * time.Second
	for {
		identity, err := s.ensureRegistered(ctx, client)
		if err == nil {
			return identity, nil
		}
		if ctx.Err() != nil {
			return agentidentity.AgentIdentity{}, ctx.Err()
		}
		if !isRetryable(err) {
			return agentidentity.AgentIdentity{}, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return agentidentity.AgentIdentity{}, fmt.Errorf("agent registration did not complete within %s: %w", s.config.StartupTimeout, err)
		}
		wait := delay
		if wait > remaining {
			wait = remaining
		}
		s.logger.Warn("agent registration request failed; retrying with the same enrollment request ID", "error", err, "retry_in", wait)
		if err := waitContext(ctx, wait); err != nil {
			return agentidentity.AgentIdentity{}, err
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}

func (s *AgentSession) renewWithRetry(ctx context.Context, client controlplane.ControlPlane, identity agentidentity.AgentIdentity) (agentidentity.AgentIdentity, error) {
	delay := 2 * time.Second
	for {
		updated, err := s.renew(ctx, client, identity)
		if err == nil {
			return updated, nil
		}
		if ctx.Err() != nil {
			return agentidentity.AgentIdentity{}, ctx.Err()
		}
		if !isRetryable(err) {
			return agentidentity.AgentIdentity{}, err
		}
		wait, waitErr := retryDelayBeforeExpiry(delay, identity.HeartbeatInterval(), identity.LeaseExpiresAt)
		if waitErr != nil {
			return agentidentity.AgentIdentity{}, waitErr
		}
		s.logger.Warn("initial agent lease renewal failed; retrying before expiration", "agent_id", identity.AgentID, "error", err, "retry_in", wait)
		if err := waitContext(ctx, wait); err != nil {
			return agentidentity.AgentIdentity{}, err
		}
		delay = nextRetryDelay(delay, identity.HeartbeatInterval())
	}
}

func (s *AgentSession) ensureRegistered(ctx context.Context, client controlplane.ControlPlane) (agentidentity.AgentIdentity, error) {
	identity, err := s.store.Load(ctx)
	if err != nil && !errors.Is(err, agentidentity.ErrIdentityNotFound) {
		return agentidentity.AgentIdentity{}, fmt.Errorf("load agent identity: %w", err)
	}
	if err == nil {
		if validateErr := identity.Validate(); validateErr != nil {
			return agentidentity.AgentIdentity{}, fmt.Errorf("validate persisted agent identity: %w", validateErr)
		}
		if identity.Registered() {
			return identity, nil
		}
	}
	if s.config.EnrollmentToken == "" {
		return agentidentity.AgentIdentity{}, errors.New("agent is not enrolled; provide MACHINARCHY_AGENT_ENROLLMENT_TOKEN for initial registration")
	}

	if errors.Is(err, agentidentity.ErrIdentityNotFound) {
		requestID, requestErr := agentidentity.NewRequestID()
		if requestErr != nil {
			return agentidentity.AgentIdentity{}, requestErr
		}
		identity = agentidentity.AgentIdentity{SchemaVersion: agentidentity.IdentitySchemaVersion, PendingRegistrationID: requestID}
		if saveErr := s.store.Save(ctx, identity); saveErr != nil {
			return agentidentity.AgentIdentity{}, fmt.Errorf("persist pending enrollment request: %w", saveErr)
		}
	}
	if identity.PendingRegistrationID == "" {
		return agentidentity.AgentIdentity{}, errors.New("agent identity state is invalid: registration request ID is missing")
	}

	hostname, err := os.Hostname()
	if err != nil {
		return agentidentity.AgentIdentity{}, fmt.Errorf("read local hostname: %w", err)
	}
	request := controlplane.RegistrationRequest{
		RequestID:       identity.PendingRegistrationID,
		EnrollmentToken: s.config.EnrollmentToken,
		Hostname:        hostname,
		AgentVersion:    s.config.AgentVersion,
		OperatingSystem: runtime.GOOS,
		Architecture:    runtime.GOARCH,
	}
	requestCtx, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	registration, err := client.Register(requestCtx, request)
	cancel()
	if err != nil {
		return agentidentity.AgentIdentity{}, err
	}

	identity = agentidentity.AgentIdentity{
		SchemaVersion:           agentidentity.IdentitySchemaVersion,
		AgentID:                 registration.AgentID,
		HostID:                  registration.HostID,
		LeaseID:                 registration.LeaseID,
		LeaseToken:              registration.LeaseToken,
		LeaseExpiresAt:          registration.LeaseExpiresAt.UTC().Format(time.RFC3339Nano),
		HeartbeatIntervalMillis: registration.HeartbeatInterval.Milliseconds(),
	}
	if err := s.store.Save(ctx, identity); err != nil {
		return agentidentity.AgentIdentity{}, fmt.Errorf("persist registered agent identity: %w", err)
	}
	return identity, nil
}

func (s *AgentSession) renew(ctx context.Context, client controlplane.ControlPlane, identity agentidentity.AgentIdentity) (agentidentity.AgentIdentity, error) {
	if err := agentidentity.EnsureLeaseAlive(identity); err != nil {
		return agentidentity.AgentIdentity{}, err
	}
	requestID, err := agentidentity.NewRequestID()
	if err != nil {
		return agentidentity.AgentIdentity{}, err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return agentidentity.AgentIdentity{}, fmt.Errorf("read local hostname: %w", err)
	}
	request := controlplane.LeaseRenewalRequest{
		RequestID:    requestID,
		AgentID:      identity.AgentID,
		HostID:       identity.HostID,
		LeaseID:      identity.LeaseID,
		LeaseToken:   identity.LeaseToken,
		AgentVersion: s.config.AgentVersion,
		Hostname:     hostname,
		SentAt:       time.Now().UTC(),
	}
	requestCtx, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	response, err := client.RenewLease(requestCtx, request)
	cancel()
	if err != nil {
		return agentidentity.AgentIdentity{}, err
	}
	identity.LeaseExpiresAt = response.LeaseExpiresAt.UTC().Format(time.RFC3339Nano)
	identity.HeartbeatIntervalMillis = response.HeartbeatInterval.Milliseconds()
	if response.LeaseToken != "" {
		identity.LeaseToken = response.LeaseToken
	}
	if err := identity.Validate(); err != nil {
		return agentidentity.AgentIdentity{}, fmt.Errorf("validate renewed agent identity: %w", err)
	}
	if err := s.store.Save(ctx, identity); err != nil {
		return agentidentity.AgentIdentity{}, fmt.Errorf("persist renewed agent lease: %w", err)
	}
	s.logger.Debug("agent lease renewed", "agent_id", identity.AgentID, "lease_expires_at", identity.LeaseExpiresAt)
	return identity, nil
}

func isInvalidAgentEvent(err error) bool {
	var invalidEvent *events.InvalidAgentEventError
	return errors.As(err, &invalidEvent)
}

func isRetryable(err error) bool {
	var remoteErr *controlplane.RemoteError
	if errors.As(err, &remoteErr) {
		return remoteErr.Retryable
	}
	var transportErr *controlplane.TransportError
	return errors.As(err, &transportErr)
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextRetryDelay(current, heartbeatInterval time.Duration) time.Duration {
	if current < 2*time.Second {
		current = 2 * time.Second
	}
	next := current * 2
	if next > 30*time.Second {
		next = 30 * time.Second
	}
	max := heartbeatInterval / 2
	if max > 0 && next > max {
		next = max
	}
	if next < time.Second {
		next = time.Second
	}
	return next
}

func resetTimer(timer *time.Timer, delay time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(delay)
}

func parseLeaseExpiry(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, errors.New("lease expiration is empty")
	}
	return time.Parse(time.RFC3339Nano, value)
}

func retryDelayBeforeExpiry(current, heartbeatInterval time.Duration, leaseExpiresAt string) (time.Duration, error) {
	expiresAt, err := parseLeaseExpiry(leaseExpiresAt)
	if err != nil {
		return 0, fmt.Errorf("parse agent lease expiration before retry: %w", err)
	}
	remaining := time.Until(expiresAt)
	if remaining <= 0 {
		return 0, errors.New("agent lease expired; agent must be re-enrolled or the control plane must restore its identity")
	}
	delay := nextRetryDelay(current, heartbeatInterval)
	if delay >= remaining {
		delay = remaining / 2
	}
	if delay <= 0 {
		delay = time.Millisecond
	}
	return delay, nil
}
