package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/machinarchy/machinarchy-protocol/agent/v1"
)

const (
	agentEventProtocolVersion = agentv1.ProtocolVersion
	maxAgentEventBytes        = 256 * 1024
	maxPendingAgentEventBytes = 16 * 1024 * 1024
	defaultEventBufferSize    = 64
	maxEventBufferSize        = 256
	maxEventLifetime          = 24 * time.Hour
	maxEventClockSkew         = 2 * time.Minute
)

// Agent event wire types are aliases to the shared v1 contract package. Keep
// agent-specific identity validation in this package, not in the protocol.
type AgentEventType = agentv1.EventType
type AgentEventEnvelope = agentv1.EventEnvelope

const (
	AgentEventOperationRequested = agentv1.EventOperationRequested
	AgentEventOperationCancelled = agentv1.EventOperationCancelled
)

type EventSubscription interface {
	Messages() <-chan []byte
	Close() error
}

type EventSubscriber interface {
	Subscribe(context.Context, string, int) (EventSubscription, error)
}

type EventLedger interface {
	Contains(context.Context, string) (bool, error)
	Mark(context.Context, string) error
}

type AgentEventHandler interface {
	Handle(context.Context, AgentEventEnvelope) error
}

type AgentEventHandlerFunc func(context.Context, AgentEventEnvelope) error

func (f AgentEventHandlerFunc) Handle(ctx context.Context, event AgentEventEnvelope) error {
	return f(ctx, event)
}

var ErrDuplicateAgentEvent = errors.New("duplicate agent event")

type InvalidAgentEventError struct {
	reason string
}

func (e *InvalidAgentEventError) Error() string {
	if e == nil || e.reason == "" {
		return "invalid agent event"
	}
	return "invalid agent event: " + e.reason
}

type AgentEventProcessor struct {
	mu       sync.Mutex
	identity AgentIdentity
	ledger   EventLedger
	handler  AgentEventHandler
	logger   *slog.Logger
	now      func() time.Time
}

func NewAgentEventProcessor(identity AgentIdentity, ledger EventLedger, handler AgentEventHandler, logger *slog.Logger) (*AgentEventProcessor, error) {
	if err := identity.Validate(); err != nil || !identity.Registered() {
		return nil, errors.New("a registered agent identity is required for event processing")
	}
	if ledger == nil {
		return nil, errors.New("agent event ledger is required")
	}
	if handler == nil {
		return nil, errors.New("agent event handler is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AgentEventProcessor{
		identity: identity,
		ledger:   ledger,
		handler:  handler,
		logger:   logger.With("component", "agent-events"),
		now:      time.Now,
	}, nil
}

// UpdateIdentity refreshes the session identity after a successful lease
// renewal. Agent and host IDs remain immutable for this process session.
func (p *AgentEventProcessor) UpdateIdentity(identity AgentIdentity) error {
	if err := identity.Validate(); err != nil || !identity.Registered() {
		return errors.New("a valid registered agent identity is required")
	}
	if err := ensureLeaseAlive(identity); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if identity.AgentID != p.identity.AgentID || identity.HostID != p.identity.HostID {
		return errors.New("agent identity changed during an active session")
	}
	p.identity = identity
	return nil
}

// Handle validates an event, suppresses recently processed event IDs, calls
// the capability boundary, and records successful handling. It never logs or
// returns the message payload.
func (p *AgentEventProcessor) Handle(ctx context.Context, raw []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx == nil {
		return errors.New("agent event context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensureLeaseAlive(p.identity); err != nil {
		return err
	}
	event, err := DecodeAgentEvent(raw)
	if err != nil {
		return err
	}
	if err := ValidateAgentEventFor(event, p.identity, p.now()); err != nil {
		return err
	}

	seen, err := p.ledger.Contains(ctx, event.EventID)
	if err != nil {
		return fmt.Errorf("check agent event deduplication ledger: %w", err)
	}
	if seen {
		return ErrDuplicateAgentEvent
	}
	if err := p.handler.Handle(ctx, event); err != nil {
		return fmt.Errorf("handle agent event %q: %w", event.EventID, err)
	}
	if err := p.ledger.Mark(ctx, event.EventID); err != nil {
		return fmt.Errorf("record handled agent event: %w", err)
	}
	p.logger.Debug("agent event validated and handled", "event_id", event.EventID, "event_type", event.EventType, "correlation_id", event.CorrelationID)
	return nil
}

func DecodeAgentEvent(raw []byte) (AgentEventEnvelope, error) {
	if len(raw) == 0 || len(raw) > maxAgentEventBytes {
		return AgentEventEnvelope{}, &InvalidAgentEventError{reason: "message is empty or exceeds the maximum size"}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var event AgentEventEnvelope
	if err := decoder.Decode(&event); err != nil {
		return AgentEventEnvelope{}, &InvalidAgentEventError{reason: "message is not a valid versioned JSON envelope"}
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return AgentEventEnvelope{}, &InvalidAgentEventError{reason: "message must contain exactly one JSON object"}
	}
	if len(bytes.TrimSpace(event.Payload)) == 0 || len(event.Payload) > maxAgentEventBytes/2 {
		return AgentEventEnvelope{}, &InvalidAgentEventError{reason: "event payload is empty or exceeds the maximum size"}
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload == nil {
		return AgentEventEnvelope{}, &InvalidAgentEventError{reason: "event payload must be a JSON object"}
	}
	return event, nil
}

func ValidateAgentEventFor(e AgentEventEnvelope, identity AgentIdentity, now time.Time) error {
	if !identity.Registered() {
		return &InvalidAgentEventError{reason: "agent identity is not registered"}
	}
	if e.ProtocolVersion != agentEventProtocolVersion {
		return &InvalidAgentEventError{reason: "unsupported protocol version"}
	}
	if !validEventIdentifier(e.EventID) || !validEventIdentifier(e.CorrelationID) {
		return &InvalidAgentEventError{reason: "event or correlation identifier has an invalid format"}
	}
	if e.TargetAgentID != identity.AgentID || e.TargetHostID != identity.HostID {
		return &InvalidAgentEventError{reason: "event target does not match this agent identity"}
	}
	switch e.EventType {
	case AgentEventOperationRequested, AgentEventOperationCancelled:
	default:
		return &InvalidAgentEventError{reason: "unsupported event type"}
	}
	if e.IssuedAt.IsZero() || e.ExpiresAt.IsZero() {
		return &InvalidAgentEventError{reason: "event timestamps are required"}
	}
	if e.IssuedAt.After(now.Add(maxEventClockSkew)) {
		return &InvalidAgentEventError{reason: "event issue time is too far in the future"}
	}
	if !e.ExpiresAt.After(now) || !e.ExpiresAt.After(e.IssuedAt) {
		return &InvalidAgentEventError{reason: "event has expired or has an invalid expiration"}
	}
	if e.ExpiresAt.Sub(e.IssuedAt) > maxEventLifetime {
		return &InvalidAgentEventError{reason: "event lifetime exceeds the maximum allowed duration"}
	}
	return nil
}

func AgentEventsSubject(agentID string) (string, error) {
	if !validNATSSubjectToken(agentID) {
		return "", errors.New("control-plane-assigned agent ID is not a valid NATS subject token")
	}
	return agentv1.AgentEventsSubject(agentID)
}

func validNATSSubjectToken(value string) bool {
	return agentv1.ValidSubjectToken(value)
}

func validEventIdentifier(value string) bool {
	return validNATSSubjectToken(value)
}
