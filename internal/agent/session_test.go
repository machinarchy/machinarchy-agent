package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type memoryIdentityStore struct {
	value   AgentIdentity
	present bool
	saves   int
}

func (s *memoryIdentityStore) Load(context.Context) (AgentIdentity, error) {
	if !s.present {
		return AgentIdentity{}, ErrIdentityNotFound
	}
	return s.value, nil
}
func (s *memoryIdentityStore) Save(_ context.Context, value AgentIdentity) error {
	s.value, s.present = value, true
	s.saves++
	return nil
}

type fakeControlPlane struct {
	registerRequest RegistrationRequest
	registration    RegistrationResponse
	registerErr     error
	renewRequest    LeaseRenewalRequest
	renewal         LeaseRenewalResponse
	renewErr        error
}

func (f *fakeControlPlane) Register(_ context.Context, request RegistrationRequest) (RegistrationResponse, error) {
	f.registerRequest = request
	return f.registration, f.registerErr
}
func (f *fakeControlPlane) RenewLease(_ context.Context, request LeaseRenewalRequest) (LeaseRenewalResponse, error) {
	f.renewRequest = request
	return f.renewal, f.renewErr
}

func TestEnsureRegisteredPersistsPendingRequestBeforeCallingControlPlane(t *testing.T) {
	store := &memoryIdentityStore{}
	cfg := validTestConfig()
	cfg.EnrollmentToken = "enrollment-token-0123456789abcdef"
	session, err := NewAgentSession(cfg, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	control := &fakeControlPlane{registration: RegistrationResponse{
		AgentID: "agent-123", HostID: "host-456", LeaseID: "lease-789", LeaseToken: "0123456789abcdef0123456789abcdef",
		LeaseExpiresAt: time.Now().Add(time.Hour), HeartbeatInterval: 15 * time.Second,
	}}

	identity, err := session.ensureRegistered(context.Background(), control)
	if err != nil {
		t.Fatalf("ensureRegistered() error = %v", err)
	}
	if !identity.Registered() {
		t.Fatalf("identity is not registered: %#v", identity)
	}
	if control.registerRequest.RequestID == "" {
		t.Fatal("registration request ID was empty")
	}
	if control.registerRequest.EnrollmentToken != cfg.EnrollmentToken {
		t.Fatal("enrollment token was not sent")
	}
	if store.saves != 2 {
		t.Fatalf("identity saves = %d, want pending state then registered state", store.saves)
	}
	if store.value.PendingRegistrationID != "" {
		t.Fatal("pending request ID was not cleared after successful registration")
	}
}

func TestEnsureRegisteredRetainsIdempotencyKeyAfterTransportFailure(t *testing.T) {
	store := &memoryIdentityStore{}
	cfg := validTestConfig()
	cfg.EnrollmentToken = "enrollment-token-0123456789abcdef"
	session, err := NewAgentSession(cfg, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	control := &fakeControlPlane{registerErr: errors.New("temporary transport failure")}

	_, err = session.ensureRegistered(context.Background(), control)
	if err == nil {
		t.Fatal("ensureRegistered() succeeded despite transport failure")
	}
	if !store.present || store.value.PendingRegistrationID == "" {
		t.Fatal("pending registration request was not persisted")
	}
	pendingID := store.value.PendingRegistrationID
	control.registerErr = nil
	control.registration = RegistrationResponse{
		AgentID: "agent-1", HostID: "host-1", LeaseID: "lease-1", LeaseToken: "0123456789abcdef0123456789abcdef",
		LeaseExpiresAt: time.Now().Add(time.Hour), HeartbeatInterval: 15 * time.Second,
	}
	_, err = session.ensureRegistered(context.Background(), control)
	if err != nil {
		t.Fatalf("second ensureRegistered() error = %v", err)
	}
	if control.registerRequest.RequestID != pendingID {
		t.Fatalf("retry request ID = %q, want persisted %q", control.registerRequest.RequestID, pendingID)
	}
}

func TestEnsureRegisteredRequiresEnrollmentTokenForFirstRun(t *testing.T) {
	store := &memoryIdentityStore{}
	cfg := validTestConfig()
	cfg.EnrollmentToken = ""
	session, err := NewAgentSession(cfg, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.ensureRegistered(context.Background(), &fakeControlPlane{})
	if err == nil {
		t.Fatal("ensureRegistered() succeeded without enrollment token")
	}
}

func TestRenewPersistsRotatedLeaseToken(t *testing.T) {
	cfg := validTestConfig()
	store, err := NewFileIdentityStore(filepath.Join(t.TempDir(), "agent", "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewAgentSession(cfg, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	identity := AgentIdentity{
		SchemaVersion: identitySchemaVersion, AgentID: "agent-1", HostID: "host-1", LeaseID: "lease-1",
		LeaseToken: "0123456789abcdef0123456789abcdef", LeaseExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), HeartbeatIntervalMillis: 15000,
	}
	if err := store.Save(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	control := &fakeControlPlane{renewal: LeaseRenewalResponse{
		LeaseExpiresAt: time.Now().Add(time.Hour), HeartbeatInterval: 20 * time.Second,
		LeaseToken: "abcdef0123456789abcdef0123456789",
	}}
	updated, err := session.renew(context.Background(), control, identity)
	if err != nil {
		t.Fatalf("renew() error = %v", err)
	}
	if updated.LeaseToken != control.renewal.LeaseToken {
		t.Fatal("lease token was not rotated")
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LeaseToken != control.renewal.LeaseToken {
		t.Fatal("rotated lease token was not persisted")
	}
}

func TestRetryDelayIsBoundedByLeaseExpiration(t *testing.T) {
	expiresAt := time.Now().Add(4 * time.Second).UTC().Format(time.RFC3339Nano)
	delay, err := retryDelayBeforeExpiry(30*time.Second, 15*time.Second, expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if delay >= 4*time.Second {
		t.Fatalf("retry delay %s is not bounded by lease expiry", delay)
	}
}

func TestRetryDelayRejectsExpiredLease(t *testing.T) {
	expiresAt := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if _, err := retryDelayBeforeExpiry(2*time.Second, 15*time.Second, expiresAt); err == nil {
		t.Fatal("retryDelayBeforeExpiry() accepted an expired lease")
	}
}

type fakeEventSubscription struct {
	messages chan []byte
	closed   bool
}

func (s *fakeEventSubscription) Messages() <-chan []byte { return s.messages }
func (s *fakeEventSubscription) Close() error {
	s.closed = true
	return nil
}

type fakeSessionConnection struct {
	connected    atomic.Bool
	subject      string
	subscription *fakeEventSubscription
	subscribed   chan struct{}
	drained      bool
	closed       bool
	published    []OperationResult
}

func (c *fakeSessionConnection) IsConnected() bool { return c.connected.Load() }
func (c *fakeSessionConnection) Drain() error      { c.drained = true; return nil }
func (c *fakeSessionConnection) Close()            { c.closed = true }
func (c *fakeSessionConnection) Request(_ context.Context, subject string, payload []byte) ([]byte, error) {
	if subject != "machinarchy.v1.agent.lease.renew" {
		return nil, errors.New("unexpected subject")
	}
	var request renewWireRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, err
	}
	response := renewWireResponse{
		ResponseMetadata: responseMetadata{ProtocolVersion: agentProtocolVersion, RequestID: request.RequestID},
		LeaseExpiresAt:   time.Now().Add(time.Hour), HeartbeatIntervalMillis: 15000,
	}
	return json.Marshal(response)
}
func (c *fakeSessionConnection) Publish(_ context.Context, subject string, payload []byte) error {
	if subject != "machinarchy.v1.agent.agent-123.operations.results" {
		return errors.New("unexpected operation result subject")
	}
	var result OperationResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return err
	}
	c.published = append(c.published, result)
	return nil
}
func (c *fakeSessionConnection) Subscribe(_ context.Context, subject string, buffer int) (EventSubscription, error) {
	c.subject = subject
	c.subscription = &fakeEventSubscription{messages: make(chan []byte, buffer)}
	close(c.subscribed)
	return c.subscription, nil
}

func TestAgentSessionSubscribesAfterLeaseAndProcessesTargetedEvent(t *testing.T) {
	cfg := validTestConfig()
	cfg.IdentityFile = filepath.Join(t.TempDir(), "agent", "identity.json")
	identity := validTestIdentity()
	store := &memoryIdentityStore{value: identity, present: true}
	session, err := NewAgentSession(cfg, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	conn := &fakeSessionConnection{subscribed: make(chan struct{})}
	conn.connected.Store(true)
	events := make(chan ConnectionEvent, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- session.Run(ctx, conn, events) }()

	select {
	case <-conn.subscribed:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("agent did not subscribe after establishing its lease")
	}
	if conn.subject != "machinarchy.v1.agent.agent-123.events" {
		cancel()
		t.Fatalf("subscription subject = %q", conn.subject)
	}
	event := validTestEvent(identity, time.Now())
	conn.subscription.messages <- encodeTestEvent(t, event)

	deadline := time.Now().Add(2 * time.Second)
	for {
		seen, ledgerErr := session.ledger.Contains(context.Background(), event.EventID)
		if ledgerErr != nil {
			cancel()
			t.Fatalf("read event ledger: %v", ledgerErr)
		}
		if seen {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("agent did not record the handled event")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("AgentSession.Run() error = %v", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AgentSession.Run() did not stop after cancellation")
	}
	if conn.subscription == nil || !conn.subscription.closed {
		t.Fatal("event subscription was not closed")
	}
	var succeeded bool
	for _, result := range conn.published {
		if result.OperationID == "operation-001" && result.Status == OperationStatusSucceeded {
			succeeded = true
		}
	}
	if !succeeded {
		t.Fatalf("agent session did not publish a successful operation result: %#v", conn.published)
	}
}

func TestAgentSessionRequiresEventSubscriber(t *testing.T) {
	cfg := validTestConfig()
	store := &memoryIdentityStore{value: validTestIdentity(), present: true}
	session, err := NewAgentSession(cfg, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	conn := &fakeConnectionForSession{connected: true}
	// This connection supports request/reply but intentionally does not expose
	// event subscription; Phase 3 requires both capabilities.
	err = session.Run(context.Background(), conn, make(chan ConnectionEvent))
	if err == nil {
		t.Fatal("Run() succeeded without an event subscriber")
	}
}

type fakeConnectionForSession struct{ connected bool }

func (c *fakeConnectionForSession) IsConnected() bool { return c.connected }
func (c *fakeConnectionForSession) Drain() error      { return nil }
func (c *fakeConnectionForSession) Close()            {}
func (c *fakeConnectionForSession) Request(_ context.Context, subject string, payload []byte) ([]byte, error) {
	if subject != "machinarchy.v1.agent.lease.renew" {
		return nil, errors.New("unexpected subject")
	}
	var request renewWireRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, err
	}
	response := renewWireResponse{
		ResponseMetadata: responseMetadata{ProtocolVersion: agentProtocolVersion, RequestID: request.RequestID},
		LeaseExpiresAt:   time.Now().Add(time.Hour), HeartbeatIntervalMillis: 15000,
	}
	return json.Marshal(response)
}

func TestAgentSessionPausesEventConsumptionUntilLeaseRenewedAfterReconnect(t *testing.T) {
	cfg := validTestConfig()
	cfg.IdentityFile = filepath.Join(t.TempDir(), "agent", "identity.json")
	identity := validTestIdentity()
	store := &memoryIdentityStore{value: identity, present: true}
	session, err := NewAgentSession(cfg, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	conn := &fakeSessionConnection{subscribed: make(chan struct{})}
	conn.connected.Store(true)
	events := make(chan ConnectionEvent, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- session.Run(ctx, conn, events) }()

	select {
	case <-conn.subscribed:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("agent did not subscribe")
	}

	// Simulate the transport dropping before the asynchronous lifecycle callback
	// is processed. The session must also consult the connection's live state.
	conn.connected.Store(false)
	events <- ConnectionEvent{Type: EventDisconnected}
	time.Sleep(25 * time.Millisecond)

	deferredEvent := validTestEvent(identity, time.Now())
	deferredEvent.EventID = "event-after-reconnect"
	deferredEvent.Payload = json.RawMessage(`{"operation_id":"operation-after-reconnect","capability":"runtime.info","input":{},"timeout_ms":30000}`)
	conn.subscription.messages <- encodeTestEvent(t, deferredEvent)
	time.Sleep(50 * time.Millisecond)
	seen, err := session.ledger.Contains(context.Background(), deferredEvent.EventID)
	if err != nil {
		cancel()
		t.Fatalf("read event ledger: %v", err)
	}
	if seen {
		cancel()
		t.Fatal("agent processed an event while broker connectivity was unavailable")
	}

	conn.connected.Store(true)
	events <- ConnectionEvent{Type: EventReconnected}
	deadline := time.Now().Add(2 * time.Second)
	for {
		seen, err = session.ledger.Contains(context.Background(), deferredEvent.EventID)
		if err != nil {
			cancel()
			t.Fatalf("read event ledger after reconnect: %v", err)
		}
		if seen {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("agent did not resume event processing after lease renewal")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("AgentSession.Run() error = %v", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AgentSession.Run() did not stop after cancellation")
	}
	if !conn.subscription.closed {
		t.Fatal("event subscription was not closed")
	}
}
