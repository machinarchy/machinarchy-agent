package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryOperationPublisher struct {
	mu      sync.Mutex
	results []OperationResult
	taken   []bool
	updates chan OperationResult
	err     error
}

func newMemoryOperationPublisher() *memoryOperationPublisher {
	return &memoryOperationPublisher{updates: make(chan OperationResult, 64)}
}

func (p *memoryOperationPublisher) PublishOperationResult(ctx context.Context, result OperationResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.err != nil {
		return p.err
	}
	p.mu.Lock()
	p.results = append(p.results, result)
	p.taken = append(p.taken, false)
	p.mu.Unlock()
	select {
	case p.updates <- result:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *memoryOperationPublisher) snapshot() []OperationResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]OperationResult(nil), p.results...)
}

func testDispatcherConfig(workers, queue int) OperationDispatcherConfig {
	return OperationDispatcherConfig{
		Workers: workers, QueueSize: queue, MaxOperationTimeout: time.Minute,
		MaxInputBytes: 1024, MaxOutputBytes: 1024, ResultPublishTimeout: time.Second,
	}
}

func newTestDispatcher(t *testing.T, workers, queue int, registry *CapabilityRegistry, publisher OperationResultPublisher) *OperationDispatcher {
	t.Helper()
	dispatcher, err := NewOperationDispatcher(validTestIdentity(), registry, publisher, testDispatcherConfig(workers, queue), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewOperationDispatcher() error = %v", err)
	}
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	return dispatcher
}

func operationEvent(identity AgentIdentity, eventID, operationID, capability string, input string, timeout time.Duration) AgentEventEnvelope {
	payload, _ := json.Marshal(OperationRequestPayload{
		OperationID: operationID, Capability: capability,
		Input: json.RawMessage(input), TimeoutMilli: timeout.Milliseconds(),
	})
	now := time.Now().UTC()
	return AgentEventEnvelope{
		ProtocolVersion: agentEventProtocolVersion, EventID: eventID,
		EventType: AgentEventOperationRequested, TargetAgentID: identity.AgentID,
		TargetHostID: identity.HostID, CorrelationID: "corr-" + eventID,
		IssuedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute), Payload: payload,
	}
}

func cancellationEvent(identity AgentIdentity, eventID, operationID string) AgentEventEnvelope {
	payload, _ := json.Marshal(OperationCancellationPayload{OperationID: operationID, Reason: "requested by controller"})
	now := time.Now().UTC()
	return AgentEventEnvelope{
		ProtocolVersion: agentEventProtocolVersion, EventID: eventID,
		EventType: AgentEventOperationCancelled, TargetAgentID: identity.AgentID,
		TargetHostID: identity.HostID, CorrelationID: "corr-" + eventID,
		IssuedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute), Payload: payload,
	}
}

func awaitResult(t *testing.T, publisher *memoryOperationPublisher, status OperationStatus) OperationResult {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		publisher.mu.Lock()
		for index, result := range publisher.results {
			if index < len(publisher.taken) && !publisher.taken[index] && result.Status == status {
				publisher.taken[index] = true
				publisher.mu.Unlock()
				return result
			}
		}
		publisher.mu.Unlock()
		select {
		case <-publisher.updates:
		case <-deadline.C:
			t.Fatalf("timed out waiting for operation result status %q; results=%#v", status, publisher.snapshot())
		}
	}
}

type failFirstOperationPublisher struct {
	mu        sync.Mutex
	remaining int
	delegate  *memoryOperationPublisher
}

func (p *failFirstOperationPublisher) PublishOperationResult(ctx context.Context, result OperationResult) error {
	p.mu.Lock()
	if p.remaining > 0 {
		p.remaining--
		p.mu.Unlock()
		return errors.New("temporary publisher failure")
	}
	p.mu.Unlock()
	return p.delegate.PublishOperationResult(ctx, result)
}

func TestOperationAcceptancePublishFailureDoesNotTearDownExecution(t *testing.T) {
	store := NewMemoryOperationStore()
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.echo", CapabilityFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"completed":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	delegate := newMemoryOperationPublisher()
	publisher := &failFirstOperationPublisher{remaining: 3, delegate: delegate}
	dispatcher, err := NewOperationDispatcherWithStore(validTestIdentity(), registry, publisher, store, testDispatcherConfig(1, 2), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	event := operationEvent(validTestIdentity(), "event-accept-publish-failure", "operation-accept-publish-failure", "test.echo", `{}`, time.Second)
	if err := dispatcher.Handle(context.Background(), event); err != nil {
		t.Fatalf("Handle() error after acceptance publish failure = %v; durable work should continue", err)
	}
	result := awaitResult(t, delegate, OperationStatusSucceeded)
	if result.OperationID != "operation-accept-publish-failure" {
		t.Fatalf("completed operation ID = %q", result.OperationID)
	}
}

func TestOperationDispatcherRunsRegisteredCapabilityAndPublishesResults(t *testing.T) {
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.echo", CapabilityFunc(func(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
		return append(json.RawMessage(nil), input...), nil
	})); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	dispatcher := newTestDispatcher(t, 2, 4, registry, publisher)
	event := operationEvent(validTestIdentity(), "event-accept-1", "operation-1", "test.echo", `{"message":"hello"}`, 10*time.Second)
	if err := dispatcher.Handle(context.Background(), event); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	accepted := awaitResult(t, publisher, OperationStatusAccepted)
	if accepted.OperationID != "operation-1" || accepted.AgentID != "agent-123" || accepted.HostID != "host-456" {
		t.Fatalf("accepted result identity mismatch: %#v", accepted)
	}
	completed := awaitResult(t, publisher, OperationStatusSucceeded)
	var output map[string]string
	if err := json.Unmarshal(completed.Output, &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output["message"] != "hello" {
		t.Fatalf("operation output = %#v", output)
	}
}

func TestOperationDispatcherRejectsUnknownCapability(t *testing.T) {
	registry := NewCapabilityRegistry()
	publisher := newMemoryOperationPublisher()
	dispatcher := newTestDispatcher(t, 1, 1, registry, publisher)
	event := operationEvent(validTestIdentity(), "event-unknown", "operation-unknown", "host.shell", `{}`, time.Second)
	if err := dispatcher.Handle(context.Background(), event); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	result := awaitResult(t, publisher, OperationStatusRejected)
	if result.ErrorCode != "capability_unavailable" {
		t.Fatalf("ErrorCode = %q", result.ErrorCode)
	}
}

func TestOperationDispatcherRejectsMalformedAndOversizedInput(t *testing.T) {
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.noop", CapabilityFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	dispatcher := newTestDispatcher(t, 1, 1, registry, publisher)

	for _, payload := range []string{
		`{"operation_id":"bad-1","capability":"test.noop","input":[],"timeout_ms":1000}`,
		`{"operation_id":"bad-2","capability":"test.noop","input":{},"unexpected":true}`,
		`{"operation_id":"bad-3","capability":"test.noop","input":{},"timeout_ms":-1}`,
	} {
		event := operationEvent(validTestIdentity(), "event-malformed", "operation-temp", "test.noop", `{}`, time.Second)
		event.Payload = json.RawMessage(payload)
		if err := dispatcher.Handle(context.Background(), event); err == nil {
			t.Fatalf("Handle() accepted malformed payload %s", payload)
		}
	}
	if got := len(publisher.snapshot()); got != 0 {
		t.Fatalf("published %d results for invalid requests", got)
	}
}

func TestOperationDispatcherEnforcesBoundedQueue(t *testing.T) {
	started := make(chan struct{}, 1)
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.block", CapabilityFunc(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	dispatcher := newTestDispatcher(t, 1, 1, registry, publisher)
	identity := validTestIdentity()

	if err := dispatcher.Handle(context.Background(), operationEvent(identity, "event-b1", "operation-b1", "test.block", `{}`, 30*time.Second)); err != nil {
		t.Fatal(err)
	}
	awaitResult(t, publisher, OperationStatusAccepted)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first operation did not start")
	}
	if err := dispatcher.Handle(context.Background(), operationEvent(identity, "event-b2", "operation-b2", "test.block", `{}`, 30*time.Second)); err != nil {
		t.Fatal(err)
	}
	awaitResult(t, publisher, OperationStatusAccepted)
	if err := dispatcher.Handle(context.Background(), operationEvent(identity, "event-b3", "operation-b3", "test.block", `{}`, 30*time.Second)); err != nil {
		t.Fatalf("queue saturation should publish rejection, not crash dispatcher: %v", err)
	}
	rejected := awaitResult(t, publisher, OperationStatusRejected)
	if rejected.ErrorCode != "operation_queue_full" {
		t.Fatalf("ErrorCode = %q, want operation_queue_full", rejected.ErrorCode)
	}
	if err := dispatcher.Handle(context.Background(), cancellationEvent(identity, "event-b1-cancel", "operation-b1")); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Handle(context.Background(), cancellationEvent(identity, "event-b2-cancel", "operation-b2")); err != nil {
		t.Fatal(err)
	}
	awaitResult(t, publisher, OperationStatusCancelled)
	awaitResult(t, publisher, OperationStatusCancelled)
}

func TestOperationDispatcherCancelsRunningOperation(t *testing.T) {
	started := make(chan struct{}, 1)
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.wait", CapabilityFunc(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	dispatcher := newTestDispatcher(t, 1, 2, registry, publisher)
	identity := validTestIdentity()
	if err := dispatcher.Handle(context.Background(), operationEvent(identity, "event-c1", "operation-c1", "test.wait", `{}`, 30*time.Second)); err != nil {
		t.Fatal(err)
	}
	awaitResult(t, publisher, OperationStatusAccepted)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("operation did not start")
	}
	if err := dispatcher.Handle(context.Background(), cancellationEvent(identity, "event-cancel-1", "operation-c1")); err != nil {
		t.Fatalf("cancellation Handle() error = %v", err)
	}
	result := awaitResult(t, publisher, OperationStatusCancelled)
	if result.ErrorCode != "operation_cancelled" {
		t.Fatalf("ErrorCode = %q", result.ErrorCode)
	}
}

func TestOperationDispatcherAppliesPerOperationTimeout(t *testing.T) {
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.timeout", CapabilityFunc(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	dispatcher := newTestDispatcher(t, 1, 1, registry, publisher)
	if err := dispatcher.Handle(context.Background(), operationEvent(validTestIdentity(), "event-timeout", "operation-timeout", "test.timeout", `{}`, 30*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	awaitResult(t, publisher, OperationStatusAccepted)
	result := awaitResult(t, publisher, OperationStatusTimedOut)
	if result.ErrorCode != "operation_timeout" {
		t.Fatalf("ErrorCode = %q", result.ErrorCode)
	}
}

func TestOperationDispatcherSuppressesDuplicateOperationID(t *testing.T) {
	started := make(chan struct{}, 1)
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.once", CapabilityFunc(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	dispatcher := newTestDispatcher(t, 1, 2, registry, publisher)
	identity := validTestIdentity()
	if err := dispatcher.Handle(context.Background(), operationEvent(identity, "event-once-a", "operation-once", "test.once", `{}`, 30*time.Second)); err != nil {
		t.Fatal(err)
	}
	awaitResult(t, publisher, OperationStatusAccepted)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("operation did not start")
	}
	if err := dispatcher.Handle(context.Background(), operationEvent(identity, "event-once-b", "operation-once", "test.once", `{}`, 30*time.Second)); err != nil {
		t.Fatalf("duplicate request Handle() error = %v", err)
	}
	if got := len(publisher.snapshot()); got != 1 {
		t.Fatalf("duplicate operation published extra result; count=%d", got)
	}
	if err := dispatcher.Handle(context.Background(), cancellationEvent(identity, "event-once-cancel", "operation-once")); err != nil {
		t.Fatal(err)
	}
	awaitResult(t, publisher, OperationStatusCancelled)
}

func TestOperationDispatcherCancellationBeforeRequestCreatesTombstone(t *testing.T) {
	called := false
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.never", CapabilityFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		called = true
		return json.RawMessage(`{}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	dispatcher := newTestDispatcher(t, 1, 1, registry, publisher)
	identity := validTestIdentity()
	if err := dispatcher.Handle(context.Background(), cancellationEvent(identity, "event-cancel-before", "operation-late")); err != nil {
		t.Fatal(err)
	}
	awaitResult(t, publisher, OperationStatusCancelled)
	if err := dispatcher.Handle(context.Background(), operationEvent(identity, "event-late", "operation-late", "test.never", `{}`, time.Second)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if called {
		t.Fatal("operation executed after cancellation tombstone")
	}
}

func TestCapabilityRegistryRejectsDuplicateAndInvalidNames(t *testing.T) {
	registry := NewCapabilityRegistry()
	noop := CapabilityFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	if err := registry.Register("runtime.info", noop); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("runtime.info", noop); err == nil {
		t.Fatal("duplicate capability registration succeeded")
	}
	for _, name := range []string{"", "foo.*", "foo..bar", "foo bar", "foo>"} {
		if err := registry.Register(name, noop); err == nil {
			t.Errorf("Register(%q) succeeded", name)
		}
	}
}

func TestOperationDispatcherContainsCapabilityPanic(t *testing.T) {
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.panic", CapabilityFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		panic("sensitive panic payload should not escape")
	})); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	dispatcher := newTestDispatcher(t, 1, 1, registry, publisher)
	if err := dispatcher.Handle(context.Background(), operationEvent(validTestIdentity(), "event-panic", "operation-panic", "test.panic", `{}`, time.Second)); err != nil {
		t.Fatal(err)
	}
	awaitResult(t, publisher, OperationStatusAccepted)
	result := awaitResult(t, publisher, OperationStatusFailed)
	if result.ErrorCode != "capability_panicked" {
		t.Fatalf("ErrorCode = %q", result.ErrorCode)
	}
}

func TestOperationDispatcherRejectsResultOutputAboveLimit(t *testing.T) {
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.large", CapabilityFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(fmt.Sprintf(`{"data":%q}`, strings.Repeat("x", 2048))), nil
	})); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	config := testDispatcherConfig(1, 1)
	config.MaxOutputBytes = 64
	dispatcher, err := NewOperationDispatcher(validTestIdentity(), registry, publisher, config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()
	if err := dispatcher.Handle(context.Background(), operationEvent(validTestIdentity(), "event-output", "operation-output", "test.large", `{}`, time.Second)); err != nil {
		t.Fatal(err)
	}
	awaitResult(t, publisher, OperationStatusAccepted)
	result := awaitResult(t, publisher, OperationStatusFailed)
	if result.ErrorCode != "operation_output_exceeded_limit" {
		t.Fatalf("ErrorCode = %q", result.ErrorCode)
	}
}

func TestAgentOperationResultsSubjectIsAgentScoped(t *testing.T) {
	subject, err := AgentOperationResultsSubject("agent-123")
	if err != nil {
		t.Fatal(err)
	}
	if subject != "machinarchy.v1.agent.agent-123.operations.results" {
		t.Fatalf("subject = %q", subject)
	}
	if _, err := AgentOperationResultsSubject("agent.*"); err == nil {
		t.Fatal("wildcard agent ID was accepted")
	}
}

func TestOperationDispatcherRequiresRegisteredIdentityAndPublisher(t *testing.T) {
	registry := NewCapabilityRegistry()
	config := testDispatcherConfig(1, 1)
	if _, err := NewOperationDispatcher(AgentIdentity{}, registry, newMemoryOperationPublisher(), config, nil); err == nil {
		t.Fatal("dispatcher accepted an unregistered identity")
	}
	if _, err := NewOperationDispatcher(validTestIdentity(), registry, nil, config, nil); err == nil {
		t.Fatal("dispatcher accepted a nil result publisher")
	}
}

func TestOperationDispatcherShutdownStopsAdmissionAndDrainsQueue(t *testing.T) {
	started := make(chan struct{}, 1)
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.drain", CapabilityFunc(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
			return json.RawMessage(`{"done":true}`), nil
		}
	})); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	dispatcher := newTestDispatcher(t, 1, 2, registry, publisher)
	if err := dispatcher.Handle(context.Background(), operationEvent(validTestIdentity(), "event-drain", "operation-drain", "test.drain", `{}`, time.Second)); err != nil {
		t.Fatal(err)
	}
	awaitResult(t, publisher, OperationStatusAccepted)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("operation did not start")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := dispatcher.Handle(context.Background(), operationEvent(validTestIdentity(), "event-after-drain", "operation-after-drain", "test.drain", `{}`, time.Second)); err == nil {
		t.Fatal("dispatcher accepted an operation after shutdown")
	}
}

func TestOperationDispatcherDrainsAfterParentContextCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	capabilityContext := make(chan context.Context, 1)
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.graceful", CapabilityFunc(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		capabilityContext <- ctx
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return json.RawMessage(`{"drained":true}`), nil
		}
	})); err != nil {
		t.Fatal(err)
	}

	publisher := newMemoryOperationPublisher()
	dispatcher, err := NewOperationDispatcher(validTestIdentity(), registry, publisher, testDispatcherConfig(1, 2), nil)
	if err != nil {
		t.Fatal(err)
	}
	parent, cancelParent := context.WithCancel(context.Background())
	if err := dispatcher.Start(parent); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Handle(context.Background(), operationEvent(validTestIdentity(), "event-graceful", "operation-graceful", "test.graceful", `{}`, time.Minute)); err != nil {
		t.Fatal(err)
	}
	awaitResult(t, publisher, OperationStatusAccepted)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("operation did not start")
	}

	cancelParent()
	select {
	case ctx := <-capabilityContext:
		select {
		case <-ctx.Done():
			t.Fatal("parent cancellation interrupted a capability before graceful drain")
		default:
		}
	case <-time.After(time.Second):
		t.Fatal("capability context was not captured")
	}

	close(release)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	completed := awaitResult(t, publisher, OperationStatusSucceeded)
	if completed.OperationID != "operation-graceful" {
		t.Fatalf("completed operation ID = %q", completed.OperationID)
	}
}

func TestOperationDispatcherAbortCancelsRunningCapability(t *testing.T) {
	started := make(chan struct{}, 1)
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.abort", CapabilityFunc(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	dispatcher := newTestDispatcher(t, 1, 2, registry, publisher)
	if err := dispatcher.Handle(context.Background(), operationEvent(validTestIdentity(), "event-abort", "operation-abort", "test.abort", `{}`, time.Minute)); err != nil {
		t.Fatal(err)
	}
	awaitResult(t, publisher, OperationStatusAccepted)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("operation did not start")
	}
	abortCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dispatcher.Abort(abortCtx); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	awaitResult(t, publisher, OperationStatusCancelled)
	if err := dispatcher.Handle(context.Background(), operationEvent(validTestIdentity(), "event-after-abort", "operation-after-abort", "test.abort", `{}`, time.Second)); err == nil {
		t.Fatal("dispatcher accepted an operation after abort")
	}
}

func TestOperationDispatcherLateCancellationDoesNotOverwriteTerminalResult(t *testing.T) {
	store := NewMemoryOperationStore()
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.echo", CapabilityFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	dispatcher, err := NewOperationDispatcherWithStore(validTestIdentity(), registry, publisher, store, testDispatcherConfig(1, 2), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	event := operationEvent(validTestIdentity(), "event-late-cancel-request", "operation-late-cancel", "test.echo", `{}`, time.Second)
	if err := dispatcher.Handle(context.Background(), event); err != nil {
		t.Fatalf("Handle(request) error = %v", err)
	}
	awaitResult(t, publisher, OperationStatusAccepted)
	awaitResult(t, publisher, OperationStatusSucceeded)

	// Simulate the bounded in-memory history evicting a terminal record while
	// the durable operation record remains available for deduplication.
	dispatcher.mu.Lock()
	delete(dispatcher.records, "operation-late-cancel")
	dispatcher.mu.Unlock()

	if err := dispatcher.Handle(context.Background(), cancellationEvent(validTestIdentity(), "event-late-cancel", "operation-late-cancel")); err != nil {
		t.Fatalf("Handle(late cancellation) error = %v", err)
	}
	stored, ok, err := store.Get(context.Background(), "operation-late-cancel")
	if err != nil || !ok {
		t.Fatalf("store.Get() = %#v, %v, %v", stored, ok, err)
	}
	if stored.Status != OperationStatusSucceeded || stored.Result == nil || stored.Result.Status != OperationStatusSucceeded {
		t.Fatalf("late cancellation overwrote terminal operation: %#v", stored)
	}
}

func TestQueueFullRejectionIsPersistedInResultOutbox(t *testing.T) {
	started := make(chan struct{}, 1)
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.block", CapabilityFunc(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	store := NewMemoryOperationStore()
	dispatcher, err := NewOperationDispatcherWithStore(validTestIdentity(), registry, publisher, store, testDispatcherConfig(1, 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dispatcher.Abort(ctx); err != nil {
			t.Errorf("Abort() error = %v", err)
		}
	}()

	identity := validTestIdentity()
	if err := dispatcher.Handle(context.Background(), operationEvent(identity, "event-qf-1", "operation-qf-1", "test.block", `{}`, 30*time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first operation did not start")
	}
	if err := dispatcher.Handle(context.Background(), operationEvent(identity, "event-qf-2", "operation-qf-2", "test.block", `{}`, 30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Handle(context.Background(), operationEvent(identity, "event-qf-3", "operation-qf-3", "test.block", `{}`, 30*time.Second)); err != nil {
		t.Fatal(err)
	}

	stored, exists, err := store.Get(context.Background(), "operation-qf-3")
	if err != nil || !exists {
		t.Fatalf("queue-full operation was not persisted: exists=%v err=%v", exists, err)
	}
	if stored.Status != OperationStatusRejected || stored.Result == nil || stored.Result.ErrorCode != "operation_queue_full" {
		t.Fatalf("queue-full operation result is not durable: %#v", stored)
	}
	pending, err := store.PendingResults(context.Background(), time.Now().UTC().Add(resultRetryInterval+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, result := range pending {
		if result.OperationID == "operation-qf-3" {
			found = true
		}
	}
	if !found {
		t.Fatal("queue-full rejection is missing from the durable result outbox")
	}
}
