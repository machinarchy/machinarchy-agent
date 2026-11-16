package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/machinarchy/machinarchy-protocol/agent/v1"
)

func acceptedStoreEntry(t *testing.T, operationID string) StoredOperation {
	t.Helper()
	event := operationEvent(validTestIdentity(), "event-store-"+operationID, operationID, "test.echo", `{"value":"hello"}`, 30*time.Second)
	request, err := decodeOperationRequest(event.Payload, maxOperationInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	return StoredOperation{
		OperationID: operationID,
		Capability:  request.Capability,
		Request:     request,
		Event:       event,
		Status:      OperationStatusAccepted,
		AcceptedAt:  time.Now().UTC(),
	}
}

func TestFileOperationStorePersistsLifecycleAndResultAcknowledgement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "operations.json")
	store, err := NewFileOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	entry := acceptedStoreEntry(t, "operation-store-1")
	if _, created, err := store.Accept(ctx, entry); err != nil || !created {
		t.Fatalf("Accept() = created %v, error %v; want true, nil", created, err)
	}
	if err := store.MarkRunning(ctx, entry.OperationID, time.Now().UTC()); err != nil {
		t.Fatalf("MarkRunning() error = %v", err)
	}
	started, ok, err := store.Get(ctx, entry.OperationID)
	if err != nil || !ok || len(started.Request.Input) != 0 || len(started.Event.Payload) != 0 || started.RequestDigest == "" {
		t.Fatalf("MarkRunning() retained raw input or lost dedupe digest: %#v, %v, %v", started, ok, err)
	}
	result := OperationResult{
		ProtocolVersion: operationProtocolVersion,
		AgentID:         validTestIdentity().AgentID,
		HostID:          validTestIdentity().HostID,
		OperationID:     entry.OperationID,
		CorrelationID:   entry.Event.CorrelationID,
		Capability:      entry.Capability,
		Status:          OperationStatusSucceeded,
		Output:          json.RawMessage(`{"ok":true}`),
		AcceptedAt:      entry.AcceptedAt,
		StartedAt:       time.Now().UTC(),
		CompletedAt:     time.Now().UTC(),
	}
	if err := store.Complete(ctx, entry.OperationID, result); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("operation store file permissions = %o, want 600", got)
	}

	reloaded, err := NewFileOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := reloaded.PendingResults(ctx, time.Now().UTC())
	if err != nil || len(pending) != 1 || pending[0].OperationID != entry.OperationID {
		t.Fatalf("PendingResults() = %#v, %v; want one persisted result", pending, err)
	}
	reloadedEntry, ok, err := reloaded.Get(ctx, entry.OperationID)
	if err != nil || !ok || (len(reloadedEntry.Request.Input) > 0 && string(reloadedEntry.Request.Input) != "null") || (len(reloadedEntry.Event.Payload) > 0 && string(reloadedEntry.Event.Payload) != "null") {
		t.Fatalf("reloaded terminal operation retained raw input: %#v, %v, %v", reloadedEntry, ok, err)
	}
	if err := reloaded.MarkResultPublished(ctx, entry.OperationID, time.Now().UTC()); err != nil {
		t.Fatalf("MarkResultPublished() error = %v", err)
	}
	pending, err = reloaded.PendingResults(ctx, time.Now().UTC())
	if err != nil || len(pending) != 0 {
		t.Fatalf("PendingResults() immediately after publish = %#v, %v; want empty during retry interval", pending, err)
	}
	pending, err = reloaded.PendingResults(ctx, time.Now().Add(resultRetryInterval+time.Second))
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingResults() after retry interval = %#v, %v; want one retry", pending, err)
	}
	if err := reloaded.AcknowledgeResult(ctx, entry.OperationID); err != nil {
		t.Fatalf("AcknowledgeResult() error = %v", err)
	}
	// The controller may acknowledge between the broker publish and the
	// publisher recording its last-published timestamp. This must be benign.
	if err := reloaded.MarkResultPublished(ctx, entry.OperationID, time.Now().UTC()); err != nil {
		t.Fatalf("MarkResultPublished() after acknowledgement error = %v", err)
	}
	pending, err = reloaded.PendingResults(ctx, time.Now().Add(time.Hour))
	if err != nil || len(pending) != 0 {
		t.Fatalf("PendingResults() after acknowledgement = %#v, %v; want empty", pending, err)
	}
	stored, ok, err := reloaded.Get(ctx, entry.OperationID)
	if err != nil || !ok || !stored.ResultAcknowledged || stored.Result != nil || stored.Status != OperationStatusSucceeded {
		t.Fatalf("Get() after acknowledgement = %#v, %v, %v", stored, ok, err)
	}
}

func TestFileOperationStoreAcceptIsIdempotentAndRejectsConflictingReuse(t *testing.T) {
	store, err := NewFileOperationStore(filepath.Join(t.TempDir(), "agent", "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	entry := acceptedStoreEntry(t, "operation-store-2")
	if _, created, err := store.Accept(ctx, entry); err != nil || !created {
		t.Fatalf("first Accept() = %v, %v", created, err)
	}
	if _, created, err := store.Accept(ctx, entry); err != nil || created {
		t.Fatalf("duplicate Accept() = %v, %v; want false, nil", created, err)
	}
	conflict := entry
	conflict.Request.Input = json.RawMessage(`{"value":"different"}`)
	if _, _, err := store.Accept(ctx, conflict); err == nil {
		t.Fatal("Accept() allowed the same operation ID with different input")
	}
}

func TestFileOperationStoreRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "operations.json")
	data, err := json.Marshal(operationStoreDocument{SchemaVersion: operationStoreSchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background()); err == nil {
		t.Fatal("List() succeeded with broad file permissions")
	}
}

func TestFileOperationStoreKeepsDeduplicationDigestAfterAcknowledgement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "operations.json")
	store, err := NewFileOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	entry := acceptedStoreEntry(t, "operation-store-digest")
	if _, created, err := store.Accept(ctx, entry); err != nil || !created {
		t.Fatalf("Accept() = %v, %v", created, err)
	}
	result := OperationResult{OperationID: entry.OperationID, Status: OperationStatusSucceeded, CompletedAt: time.Now().UTC()}
	if err := store.Complete(ctx, entry.OperationID, result); err != nil {
		t.Fatal(err)
	}
	if err := store.AcknowledgeResult(ctx, entry.OperationID); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewFileOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, created, err := reloaded.Accept(ctx, entry)
	inputRetained := len(duplicate.Request.Input) > 0 && string(duplicate.Request.Input) != "null"
	if err != nil || created || inputRetained || duplicate.RequestDigest == "" {
		t.Fatalf("duplicate Accept() after acknowledgement = %#v, %v, %v", duplicate, created, err)
	}
}

func TestOperationDispatcherRecoversRunningOperationAsUnknownWithoutRerun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "operations.json")
	store, err := NewFileOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := acceptedStoreEntry(t, "operation-recover-running")
	if _, _, err := store.Accept(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(context.Background(), entry.OperationID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	var executions atomic.Int32
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.echo", CapabilityFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		executions.Add(1)
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	reloaded, err := NewFileOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewOperationDispatcherWithStore(validTestIdentity(), registry, publisher, reloaded, testDispatcherConfig(1, 2), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	}()
	result := awaitResult(t, publisher, OperationStatusUnknown)
	if result.ErrorCode != "agent_restarted_during_execution" {
		t.Fatalf("unknown result error code = %q", result.ErrorCode)
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("capability executions after recovering running operation = %d, want 0", got)
	}
}

func TestOperationDispatcherRecoversAcceptedOperationWithoutDuplicateExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "operations.json")
	store, err := NewFileOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := acceptedStoreEntry(t, "operation-recover-accepted")
	if _, created, err := store.Accept(context.Background(), entry); err != nil || !created {
		t.Fatalf("Accept() = %v, %v", created, err)
	}

	var executions atomic.Int32
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.echo", CapabilityFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		executions.Add(1)
		return json.RawMessage(`{"ran":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	reloaded, err := NewFileOperationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewOperationDispatcherWithStore(validTestIdentity(), registry, publisher, reloaded, testDispatcherConfig(1, 2), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	}()
	awaitResult(t, publisher, OperationStatusSucceeded)
	if got := executions.Load(); got != 1 {
		t.Fatalf("capability executions after recovery = %d, want 1", got)
	}
}

func TestOperationResultAcknowledgementProtocolTypeIsStable(t *testing.T) {
	payload, err := json.Marshal(agentv1.OperationResultAcknowledgement{OperationID: "operation-ack-1"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded agentv1.OperationResultAcknowledgement
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.OperationID != "operation-ack-1" {
		t.Fatalf("acknowledgement round trip = %#v, %v", decoded, err)
	}
}

func TestTrackedOperationCountReleasesAcknowledgedUnknownResults(t *testing.T) {
	entries := map[string]StoredOperation{
		"unknown-pending": {
			OperationID: "unknown-pending",
			Status:      OperationStatusUnknown,
			Result:      &OperationResult{OperationID: "unknown-pending", Status: OperationStatusUnknown},
		},
		"unknown-acked": {
			OperationID:        "unknown-acked",
			Status:             OperationStatusUnknown,
			ResultAcknowledged: true,
		},
	}
	if got := trackedOperationCount(entries); got != 1 {
		t.Fatalf("trackedOperationCount() = %d, want 1 for only the unacknowledged unknown result", got)
	}
}

func TestOperationDispatcherRejectsRecoveryBacklogLargerThanQueue(t *testing.T) {
	store := NewMemoryOperationStore()
	for _, operationID := range []string{"operation-backlog-1", "operation-backlog-2"} {
		entry := acceptedStoreEntry(t, operationID)
		if _, created, err := store.Accept(context.Background(), entry); err != nil || !created {
			t.Fatalf("store.Accept(%s) = %v, %v", operationID, created, err)
		}
	}
	registry := NewCapabilityRegistry()
	if err := registry.Register("test.echo", CapabilityFunc(func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewOperationDispatcherWithStore(validTestIdentity(), registry, newMemoryOperationPublisher(), store, testDispatcherConfig(1, 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds configured queue capacity") {
		t.Fatalf("Start() error = %v, want an actionable recovery backlog error", err)
	}
}
