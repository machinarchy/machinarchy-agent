package events

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/identity"
)

func validTestIdentity() identity.AgentIdentity {
	return identity.AgentIdentity{
		SchemaVersion:           identity.IdentitySchemaVersion,
		AgentID:                 "agent-123",
		HostID:                  "host-456",
		LeaseID:                 "lease-789",
		LeaseToken:              "0123456789abcdef0123456789abcdef",
		LeaseExpiresAt:          time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
		HeartbeatIntervalMillis: 15000,
	}
}

func validTestEvent(identity identity.AgentIdentity, now time.Time) AgentEventEnvelope {
	return AgentEventEnvelope{
		ProtocolVersion: agentEventProtocolVersion,
		EventID:         "event-001",
		EventType:       AgentEventOperationRequested,
		TargetAgentID:   identity.AgentID,
		TargetHostID:    identity.HostID,
		CorrelationID:   "corr-001",
		IssuedAt:        now.Add(-time.Second).UTC(),
		ExpiresAt:       now.Add(time.Minute).UTC(),
		Payload:         json.RawMessage(`{"operation_id":"operation-001","capability":"runtime.info","input":{},"timeout_ms":30000}`),
	}
}

func encodeTestEvent(t *testing.T, event AgentEventEnvelope) []byte {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return data
}

type memoryEventLedger struct {
	seen map[string]bool
}

func (l *memoryEventLedger) Contains(_ context.Context, id string) (bool, error) {
	return l.seen[id], nil
}
func (l *memoryEventLedger) Mark(_ context.Context, id string) error {
	if l.seen == nil {
		l.seen = make(map[string]bool)
	}
	l.seen[id] = true
	return nil
}

func TestDecodeAgentEventAcceptsValidEnvelope(t *testing.T) {
	identity := validTestIdentity()
	event := validTestEvent(identity, time.Now())
	decoded, err := DecodeAgentEvent(encodeTestEvent(t, event))
	if err != nil {
		t.Fatalf("DecodeAgentEvent() error = %v", err)
	}
	if decoded.EventID != event.EventID || decoded.TargetAgentID != identity.AgentID {
		t.Fatalf("decoded event = %#v", decoded)
	}
}

func TestDecodeAgentEventRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	identity := validTestIdentity()
	event := validTestEvent(identity, time.Now())
	data := encodeTestEvent(t, event)
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	body["unexpected"] = true
	withUnknown, _ := json.Marshal(body)
	if _, err := DecodeAgentEvent(withUnknown); err == nil {
		t.Fatal("DecodeAgentEvent() accepted an unknown envelope field")
	}
	if _, err := DecodeAgentEvent(append(data, []byte(` {}`)...)); err == nil {
		t.Fatal("DecodeAgentEvent() accepted multiple JSON values")
	}
}

func TestAgentEventValidateForRejectsWrongIdentity(t *testing.T) {
	identity := validTestIdentity()
	event := validTestEvent(identity, time.Now())
	event.TargetAgentID = "another-agent"
	if err := ValidateAgentEventFor(event, identity, time.Now()); err == nil {
		t.Fatal("ValidateFor() accepted an event for another agent")
	}
	event = validTestEvent(identity, time.Now())
	event.TargetHostID = "another-host"
	if err := ValidateAgentEventFor(event, identity, time.Now()); err == nil {
		t.Fatal("ValidateFor() accepted an event for another host")
	}
}

func TestAgentEventValidateForRejectsExpiredUnsupportedAndLongLivedEvents(t *testing.T) {
	identity := validTestIdentity()
	now := time.Now()

	event := validTestEvent(identity, now)
	event.ExpiresAt = now.Add(-time.Second)
	if err := ValidateAgentEventFor(event, identity, now); err == nil {
		t.Fatal("ValidateFor() accepted an expired event")
	}

	event = validTestEvent(identity, now)
	event.ProtocolVersion++
	if err := ValidateAgentEventFor(event, identity, now); err == nil {
		t.Fatal("ValidateFor() accepted an unsupported protocol version")
	}

	event = validTestEvent(identity, now)
	event.EventType = "host.delete"
	if err := ValidateAgentEventFor(event, identity, now); err == nil {
		t.Fatal("ValidateFor() accepted an unsupported event type")
	}

	event = validTestEvent(identity, now)
	event.ExpiresAt = event.IssuedAt.Add(maxEventLifetime + time.Second)
	if err := ValidateAgentEventFor(event, identity, now); err == nil {
		t.Fatal("ValidateFor() accepted an event lifetime above the limit")
	}

	event = validTestEvent(identity, now)
	event.IssuedAt = now.Add(maxEventClockSkew + time.Second)
	if err := ValidateAgentEventFor(event, identity, now); err == nil {
		t.Fatal("ValidateFor() accepted a future-issued event")
	}
}

func TestDecodeAgentEventRejectsNonObjectPayload(t *testing.T) {
	identity := validTestIdentity()
	event := validTestEvent(identity, time.Now())
	event.Payload = json.RawMessage(`[]`)
	if _, err := DecodeAgentEvent(encodeTestEvent(t, event)); err == nil {
		t.Fatal("DecodeAgentEvent() accepted a non-object payload")
	}
}

func TestAgentEventsSubjectUsesControlPlaneIdentity(t *testing.T) {
	subject, err := AgentEventsSubject("agent-123")
	if err != nil {
		t.Fatalf("AgentEventsSubject() error = %v", err)
	}
	if subject != "machinarchy.v1.agent.agent-123.events" {
		t.Fatalf("subject = %q", subject)
	}
	for _, invalid := range []string{"", "agent.one", "agent*", "agent>", "agent id"} {
		if _, err := AgentEventsSubject(invalid); err == nil {
			t.Errorf("AgentEventsSubject(%q) succeeded", invalid)
		}
	}
}

func TestAgentEventProcessorSuppressesDuplicateAndDoesNotLogPayload(t *testing.T) {
	identity := validTestIdentity()
	ledger := &memoryEventLedger{seen: make(map[string]bool)}
	handled := 0
	handler := AgentEventHandlerFunc(func(_ context.Context, _ AgentEventEnvelope) error {
		handled++
		return nil
	})
	processor, err := NewAgentEventProcessor(identity, ledger, handler, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	data := encodeTestEvent(t, validTestEvent(identity, time.Now()))
	if err := processor.Handle(context.Background(), data); err != nil {
		t.Fatalf("first Handle() error = %v", err)
	}
	if err := processor.Handle(context.Background(), data); !errors.Is(err, ErrDuplicateAgentEvent) {
		t.Fatalf("second Handle() error = %v, want ErrDuplicateAgentEvent", err)
	}
	if handled != 1 {
		t.Fatalf("handler invoked %d times, want 1", handled)
	}
}

func TestAgentEventProcessorDoesNotRecordFailedHandler(t *testing.T) {
	identity := validTestIdentity()
	ledger := &memoryEventLedger{seen: make(map[string]bool)}
	processor, err := NewAgentEventProcessor(identity, ledger, AgentEventHandlerFunc(func(context.Context, AgentEventEnvelope) error {
		return errors.New("simulated handler error")
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	event := validTestEvent(identity, time.Now())
	if err := processor.Handle(context.Background(), encodeTestEvent(t, event)); err == nil {
		t.Fatal("Handle() succeeded despite handler failure")
	}
	if ledger.seen[event.EventID] {
		t.Fatal("failed event was marked as handled")
	}
}

func TestFileEventLedgerPersistsDeduplicationWindowWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "events.json")
	ledger, err := NewFileEventLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seen, err := ledger.Contains(ctx, "event-001")
	if err != nil || seen {
		t.Fatalf("Contains() = %v, %v; want false, nil", seen, err)
	}
	if err := ledger.Mark(ctx, "event-001"); err != nil {
		t.Fatalf("Mark() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("event ledger permissions = %o, want 600", info.Mode().Perm())
	}

	reloaded, err := NewFileEventLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	seen, err = reloaded.Contains(ctx, "event-001")
	if err != nil || !seen {
		t.Fatalf("reloaded Contains() = %v, %v; want true, nil", seen, err)
	}
}

func TestFileEventLedgerRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"event_ids":["event-001"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ledger, err := NewFileEventLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Contains(context.Background(), "event-001"); err == nil {
		t.Fatal("Contains() succeeded with broad file permissions")
	}
}

func TestFileEventLedgerRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"schema_version":1,"event_ids":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "events.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	ledger, err := NewFileEventLedger(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Contains(context.Background(), "event-001"); err == nil {
		t.Fatal("Contains() succeeded for a symlinked ledger")
	}
}

func TestAgentEventProcessorRejectsExpiredLease(t *testing.T) {
	identity := validTestIdentity()
	identity.LeaseExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	processor, err := NewAgentEventProcessor(identity, &memoryEventLedger{seen: make(map[string]bool)}, AgentEventHandlerFunc(func(context.Context, AgentEventEnvelope) error { return nil }), nil)
	if err != nil {
		t.Fatal(err)
	}
	event := validTestEvent(identity, time.Now())
	if err := processor.Handle(context.Background(), encodeTestEvent(t, event)); !errors.Is(err, identity.ErrLeaseExpired) {
		t.Fatalf("Handle() error = %v, want identity.ErrLeaseExpired", err)
	}
}

func TestAgentEventProcessorUpdatesRenewedLease(t *testing.T) {
	identity := validTestIdentity()
	processor, err := NewAgentEventProcessor(identity, &memoryEventLedger{seen: make(map[string]bool)}, AgentEventHandlerFunc(func(context.Context, AgentEventEnvelope) error { return nil }), nil)
	if err != nil {
		t.Fatal(err)
	}
	updated := identity
	updated.LeaseExpiresAt = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	updated.HeartbeatIntervalMillis = 20000
	if err := processor.UpdateIdentity(updated); err != nil {
		t.Fatalf("UpdateIdentity() error = %v", err)
	}
	changed := updated
	changed.AgentID = "some-other-agent"
	if err := processor.UpdateIdentity(changed); err == nil {
		t.Fatal("UpdateIdentity() accepted a changed agent identity")
	}
}
