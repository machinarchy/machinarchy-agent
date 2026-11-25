package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/capabilities"
	"github.com/machinarchy/machinarchy-agent/internal/state"
	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// recorder is a Publisher that keeps every result and can be told to fail.
type recorder struct {
	mu      sync.Mutex
	results []agentv1.Result
	fail    atomic.Bool
	notify  chan struct{}
}

func newRecorder() *recorder { return &recorder{notify: make(chan struct{}, 1024)} }

func (r *recorder) PublishResult(_ context.Context, result agentv1.Result) error {
	if r.fail.Load() {
		return errors.New("broker down")
	}
	r.mu.Lock()
	r.results = append(r.results, result)
	r.mu.Unlock()
	select {
	case r.notify <- struct{}{}:
	default:
	}
	return nil
}

// waitFor blocks until a result for operationID with a terminal status (or
// the given status) is published.
func (r *recorder) waitFor(t *testing.T, operationID string, status agentv1.OperationStatus) agentv1.Result {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		r.mu.Lock()
		for _, result := range r.results {
			if result.OperationID == operationID && result.Status == status {
				r.mu.Unlock()
				return result
			}
		}
		r.mu.Unlock()
		select {
		case <-r.notify:
		case <-deadline:
			r.mu.Lock()
			defer r.mu.Unlock()
			t.Fatalf("no %s result for %s; got %+v", status, operationID, r.results)
		}
	}
}

func (r *recorder) count(operationID string, status agentv1.OperationStatus) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, result := range r.results {
		if result.OperationID == operationID && result.Status == status {
			n++
		}
	}
	return n
}

type harness struct {
	t        *testing.T
	path     string
	store    *state.Store
	pub      *recorder
	disp     *Dispatcher
	cancel   context.CancelFunc
	done     chan error
	registry *capabilities.Registry
	opts     Options
}

var echo = capabilities.Func(func(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
	return input, nil
})

func newHarness(t *testing.T, registry *capabilities.Registry, mutate func(*Options)) *harness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "state.json")
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{AgentID: "agent-1", Workers: 2, QueueSize: 4, MaxOperationTimeout: time.Minute,
		MaxOutputBytes: 1024, ShutdownTimeout: time.Second, RepublishInterval: 50 * time.Millisecond}
	if mutate != nil {
		mutate(&opts)
	}
	h := &harness{t: t, path: path, store: store, pub: newRecorder(), registry: registry, opts: opts}
	h.start()
	t.Cleanup(h.stop)
	return h
}

func (h *harness) start() {
	disp, err := New(h.store, h.registry, h.pub, h.opts, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.disp, h.cancel, h.done = disp, cancel, make(chan error, 1)
	go func() { h.done <- disp.Run(ctx) }()
	select {
	case <-disp.Ready():
	case <-time.After(2 * time.Second):
		h.t.Fatal("dispatcher did not become ready")
	}
}

func (h *harness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Errorf("Run() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		h.t.Error("dispatcher did not stop")
	}
	h.cancel = nil
}

var commandSeq atomic.Int64

func request(operationID, capability, input string) agentv1.Command {
	now := time.Now().UTC()
	return agentv1.Command{ProtocolVersion: 1, CommandID: fmt.Sprintf("cmd-%d", commandSeq.Add(1)), AgentID: "agent-1",
		Type: agentv1.CommandOperationRequest, IssuedAt: now, ExpiresAt: now.Add(time.Minute), OperationID: operationID,
		Capability: capability, Input: json.RawMessage(input), TimeoutMillis: 10_000}
}

func control(kind agentv1.CommandType, operationID string) agentv1.Command {
	now := time.Now().UTC()
	return agentv1.Command{ProtocolVersion: 1, CommandID: fmt.Sprintf("cmd-%d", commandSeq.Add(1)), AgentID: "agent-1",
		Type: kind, IssuedAt: now, ExpiresAt: now.Add(time.Minute), OperationID: operationID}
}

func (h *harness) handle(cmd agentv1.Command) {
	h.t.Helper()
	if err := h.disp.Handle(context.Background(), cmd); err != nil {
		h.t.Fatalf("Handle(%s) error = %v", cmd.Type, err)
	}
}

func TestRequestRunsOnceAndRepublishesUntilAcked(t *testing.T) {
	var runs atomic.Int32
	counting := capabilities.Func(func(_ context.Context, in json.RawMessage) (json.RawMessage, error) {
		runs.Add(1)
		return in, nil
	})
	h := newHarness(t, capabilities.NewStatic(map[string]capabilities.Capability{"test.echo": counting}), nil)
	cmd := request("op-1", "test.echo", `{"x":1}`)
	h.handle(cmd)
	h.pub.waitFor(t, "op-1", agentv1.StatusAccepted)
	result := h.pub.waitFor(t, "op-1", agentv1.StatusSucceeded)
	if string(result.Output) != `{"x":1}` || result.StartedAt == nil || result.CompletedAt == nil || result.AgentID != "agent-1" {
		t.Fatalf("unexpected result: %+v", result)
	}
	// Duplicate command and a new command reusing the operation ID must not re-run.
	h.handle(cmd)
	h.handle(request("op-1", "test.echo", `{"x":1}`))
	// Republished until acknowledged.
	deadline := time.Now().Add(2 * time.Second)
	for h.pub.count("op-1", agentv1.StatusSucceeded) < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if h.pub.count("op-1", agentv1.StatusSucceeded) < 3 {
		t.Fatal("unacknowledged result was not republished")
	}
	h.handle(control(agentv1.CommandResultAck, "op-1"))
	time.Sleep(150 * time.Millisecond)
	before := h.pub.count("op-1", agentv1.StatusSucceeded)
	time.Sleep(200 * time.Millisecond)
	if after := h.pub.count("op-1", agentv1.StatusSucceeded); after != before {
		t.Fatalf("result republished after ack (%d -> %d)", before, after)
	}
	if runs.Load() != 1 {
		t.Fatalf("operation ran %d times, want 1", runs.Load())
	}
	if op := h.store.Snapshot().Operations["op-1"]; op == nil || !op.ResultAcked || op.Result.Output != nil {
		t.Fatalf("acked operation state = %+v", op)
	}
}

func TestOutcomeMapping(t *testing.T) {
	registry := capabilities.NewStatic(map[string]capabilities.Capability{
		"test.echo": echo,
		"test.fail": capabilities.Func(func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("boom")
		}),
		"test.panic": capabilities.Func(func(context.Context, json.RawMessage) (json.RawMessage, error) {
			panic("capability bug")
		}),
		"test.slow": capabilities.Func(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	})
	h := newHarness(t, registry, nil)
	cases := []struct {
		cmd    agentv1.Command
		status agentv1.OperationStatus
		code   string
	}{
		{request("unknown", "test.missing", `{}`), agentv1.StatusRejected, agentv1.CodeCapabilityUnavailable},
		{request("fails", "test.fail", `{}`), agentv1.StatusFailed, agentv1.CodeExecutionFailed},
		{request("panics", "test.panic", `{}`), agentv1.StatusFailed, agentv1.CodeExecutionFailed},
		{request("too-big", "test.echo", fmt.Sprintf(`{"x":"%0*d"}`, 2000, 0)), agentv1.StatusFailed, agentv1.CodeOutputTooLarge},
	}
	timeout := request("times-out", "test.slow", `{}`)
	timeout.TimeoutMillis = 50
	cases = append(cases, struct {
		cmd    agentv1.Command
		status agentv1.OperationStatus
		code   string
	}{timeout, agentv1.StatusTimedOut, agentv1.CodeTimedOut})
	for _, tc := range cases {
		h.handle(tc.cmd)
		result := h.pub.waitFor(t, tc.cmd.OperationID, tc.status)
		if result.Code != tc.code || result.Output != nil {
			t.Errorf("%s: result = %+v, want code %s without output", tc.cmd.OperationID, result, tc.code)
		}
	}
}

func TestInvalidInputIsRejected(t *testing.T) {
	registry, err := capabilities.New(capabilities.Options{AgentVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, registry, nil)
	h.handle(request("bad", agentv1.CapabilityRuntimeInfo, `{"unexpected":true}`))
	if result := h.pub.waitFor(t, "bad", agentv1.StatusRejected); result.Code != agentv1.CodeInvalidInput {
		t.Fatalf("code = %s", result.Code)
	}
	h.handle(request("ok", agentv1.CapabilityRuntimeInfo, `{}`))
	h.pub.waitFor(t, "ok", agentv1.StatusSucceeded)
}

func TestCancelQueuedAndRunning(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	blocking := capabilities.Func(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return json.RawMessage(`{}`), nil
		}
	})
	h := newHarness(t, capabilities.NewStatic(map[string]capabilities.Capability{"test.block": blocking}), func(o *Options) { o.Workers = 1 })
	defer close(release)
	h.handle(request("running", "test.block", `{}`))
	<-started
	h.handle(request("queued", "test.block", `{}`))
	h.handle(control(agentv1.CommandOperationCancel, "queued"))
	h.pub.waitFor(t, "queued", agentv1.StatusCancelled)
	h.handle(control(agentv1.CommandOperationCancel, "running"))
	h.pub.waitFor(t, "running", agentv1.StatusCancelled)
}

func TestQueueFullRejects(t *testing.T) {
	release := make(chan struct{})
	blocking := capabilities.Func(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return json.RawMessage(`{}`), nil
	})
	h := newHarness(t, capabilities.NewStatic(map[string]capabilities.Capability{"test.block": blocking}),
		func(o *Options) { o.Workers, o.QueueSize = 1, 2 })
	defer close(release)
	for i := range 3 {
		h.handle(request(fmt.Sprintf("op-%d", i), "test.block", `{}`))
	}
	if result := h.pub.waitFor(t, "op-2", agentv1.StatusRejected); result.Code != agentv1.CodeQueueFull {
		t.Fatalf("code = %s", result.Code)
	}
}

func TestExpiredBeforeExecution(t *testing.T) {
	h := newHarness(t, capabilities.NewStatic(map[string]capabilities.Capability{"test.echo": echo}), nil)
	now := time.Now()
	h.disp.opts.Now = func() time.Time { return now.Add(2 * time.Minute) }
	cmd := request("late", "test.echo", `{}`)
	cmd.ExpiresAt = now.Add(time.Minute)
	h.handle(cmd)
	if result := h.pub.waitFor(t, "late", agentv1.StatusRejected); result.Code != agentv1.CodeExpired {
		t.Fatalf("code = %s", result.Code)
	}
}

func TestRecoveryNeverReplaysRunningOperations(t *testing.T) {
	var runs atomic.Int32
	counting := capabilities.Func(func(_ context.Context, in json.RawMessage) (json.RawMessage, error) {
		runs.Add(1)
		return in, nil
	})
	h := newHarness(t, capabilities.NewStatic(map[string]capabilities.Capability{"test.echo": counting}), nil)
	h.stop()
	// Simulate a crash: one operation was running, one was queued.
	now := time.Now().UTC()
	if err := h.store.Update(func(doc *state.Document) error {
		doc.Operations["was-running"] = &state.Operation{OperationID: "was-running", Capability: "test.echo", Input: json.RawMessage(`{}`),
			TimeoutMillis: 1000, ExpiresAt: now.Add(time.Minute), Phase: state.PhaseRunning, AcceptedAt: now, StartedAt: &now}
		doc.Operations["was-queued"] = &state.Operation{OperationID: "was-queued", Capability: "test.echo", Input: json.RawMessage(`{}`),
			TimeoutMillis: 1000, ExpiresAt: now.Add(time.Minute), Phase: state.PhaseQueued, AcceptedAt: now}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(h.path)
	if err != nil {
		t.Fatal(err)
	}
	h.store = store
	h.start()
	if result := h.pub.waitFor(t, "was-running", agentv1.StatusUnknown); result.Code != agentv1.CodeAgentRestarted {
		t.Fatalf("code = %s", result.Code)
	}
	h.pub.waitFor(t, "was-queued", agentv1.StatusSucceeded)
	if runs.Load() != 1 {
		t.Fatalf("runs = %d, want only the queued operation to execute", runs.Load())
	}
}

func TestShutdownCancelsAfterBudgetAndReportsUnknown(t *testing.T) {
	started := make(chan struct{}, 1)
	stuck := capabilities.Func(func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	h := newHarness(t, capabilities.NewStatic(map[string]capabilities.Capability{"test.stuck": stuck}),
		func(o *Options) { o.ShutdownTimeout = 100 * time.Millisecond })
	h.handle(request("stuck", "test.stuck", `{}`))
	<-started
	began := time.Now()
	h.stop()
	if elapsed := time.Since(began); elapsed > 3*time.Second {
		t.Fatalf("shutdown took %s", elapsed)
	}
	op := h.store.Snapshot().Operations["stuck"]
	if op == nil || op.Result == nil || op.Result.Status != agentv1.StatusUnknown || op.Result.Code != agentv1.CodeAgentShutdown {
		t.Fatalf("operation after shutdown = %+v", op)
	}
	if err := h.disp.Handle(context.Background(), request("late", "test.stuck", `{}`)); !errors.Is(err, ErrNotAccepting) {
		t.Fatalf("Handle after shutdown = %v", err)
	}
}

func TestPublishFailureIsRetried(t *testing.T) {
	h := newHarness(t, capabilities.NewStatic(map[string]capabilities.Capability{"test.echo": echo}), nil)
	h.pub.fail.Store(true)
	h.handle(request("retry", "test.echo", `{}`))
	time.Sleep(150 * time.Millisecond)
	if h.pub.count("retry", agentv1.StatusSucceeded) != 0 {
		t.Fatal("publisher failure was ignored")
	}
	h.pub.fail.Store(false)
	h.pub.waitFor(t, "retry", agentv1.StatusSucceeded)
}

func TestRejectsCommandsForOtherAgents(t *testing.T) {
	h := newHarness(t, capabilities.NewStatic(map[string]capabilities.Capability{"test.echo": echo}), nil)
	cmd := request("x", "test.echo", `{}`)
	cmd.AgentID = "agent-2"
	if err := h.disp.Handle(context.Background(), cmd); err == nil {
		t.Fatal("command for another agent accepted")
	}
}

func TestNewValidatesOptions(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "s", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := capabilities.NewStatic(nil)
	valid := Options{AgentID: "a", Workers: 1, QueueSize: 1, MaxOperationTimeout: time.Second, MaxOutputBytes: 1, ShutdownTimeout: time.Second}
	if _, err := New(store, registry, newRecorder(), valid, nil); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Options){
		"bad agent":        func(o *Options) { o.AgentID = "a.b" },
		"workers > queue":  func(o *Options) { o.Workers = 2 },
		"no output budget": func(o *Options) { o.MaxOutputBytes = 0 },
	} {
		opts := valid
		mutate(&opts)
		if _, err := New(store, registry, newRecorder(), opts, nil); err == nil {
			t.Errorf("%s: New() succeeded", name)
		}
	}
}
