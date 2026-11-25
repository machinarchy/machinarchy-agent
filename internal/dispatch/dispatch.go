// Package dispatch executes controller commands against the local capability
// registry with bounded concurrency, durable acceptance, crash-safe recovery
// and an at-least-once result outbox.
//
// Guarantees:
//   - A command is processed at most once (replay ledger keyed by command ID).
//   - An operation runs at most once (idempotency by operation ID). If the
//     agent stops while an operation is running, the operation is reported as
//     "unknown" and never re-executed automatically.
//   - Every terminal result is republished until the controller acknowledges it.
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/capabilities"
	"github.com/machinarchy/machinarchy-agent/internal/state"
	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// ErrNotAccepting is returned while the dispatcher is starting or stopping.
// The caller must not acknowledge the command so it is redelivered.
var ErrNotAccepting = errors.New("dispatcher is not accepting commands")

// Publisher delivers a result to the controller (signing is its concern).
type Publisher interface {
	PublishResult(ctx context.Context, result agentv1.Result) error
}

// Options configures limits and timing.
type Options struct {
	AgentID             string
	Workers             int
	QueueSize           int
	MaxOperationTimeout time.Duration
	MaxOutputBytes      int
	ShutdownTimeout     time.Duration
	RepublishInterval   time.Duration
	Now                 func() time.Time
}

// Dispatcher runs operations. Create with New and drive with Run.
type Dispatcher struct {
	opts      Options
	store     *state.Store
	registry  *capabilities.Registry
	publisher Publisher
	logger    *slog.Logger

	queue chan string
	wake  chan struct{}
	ready chan struct{}

	mu        sync.Mutex
	accepting bool
	running   map[string]*execution
}

type execution struct {
	cancel        context.CancelFunc
	cancelledByOp bool
}

// New validates options and constructs a dispatcher.
func New(store *state.Store, registry *capabilities.Registry, publisher Publisher, opts Options, logger *slog.Logger) (*Dispatcher, error) {
	switch {
	case store == nil || registry == nil || publisher == nil:
		return nil, errors.New("dispatcher requires a store, registry and publisher")
	case !agentv1.ValidIdentifier(opts.AgentID):
		return nil, errors.New("dispatcher requires a valid agent ID")
	case opts.Workers < 1 || opts.QueueSize < opts.Workers:
		return nil, errors.New("dispatcher requires 1 <= workers <= queue size")
	case opts.MaxOperationTimeout <= 0 || opts.MaxOutputBytes <= 0 || opts.ShutdownTimeout <= 0:
		return nil, errors.New("dispatcher limits must be positive")
	}
	if opts.RepublishInterval <= 0 {
		opts.RepublishInterval = 10 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		opts: opts, store: store, registry: registry, publisher: publisher,
		logger: logger.With("component", "dispatch"),
		queue:  make(chan string, opts.QueueSize), wake: make(chan struct{}, 1), ready: make(chan struct{}),
		running: map[string]*execution{},
	}, nil
}

// Run recovers persisted operations, executes queued work and publishes
// results until ctx is cancelled, then drains within ShutdownTimeout.
func (d *Dispatcher) Run(ctx context.Context) error {
	if err := d.recover(); err != nil {
		return fmt.Errorf("recover operations: %w", err)
	}
	execCtx, cancelExec := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelExec()
	stopWorkers := make(chan struct{})
	var workers sync.WaitGroup
	for range d.opts.Workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			d.worker(execCtx, stopWorkers)
		}()
	}
	outboxCtx, stopOutbox := context.WithCancel(context.WithoutCancel(ctx))
	outboxDone := make(chan struct{})
	go func() {
		defer close(outboxDone)
		d.outbox(outboxCtx)
	}()

	d.setAccepting(true)
	close(d.ready)
	<-ctx.Done()
	d.setAccepting(false)
	close(stopWorkers)

	// Give running operations the shutdown budget, then cancel them. Their
	// results are recorded as unknown because the side effect may be partial.
	finished := make(chan struct{})
	go func() { workers.Wait(); close(finished) }()
	grace := time.NewTimer(d.opts.ShutdownTimeout)
	defer grace.Stop()
	select {
	case <-finished:
	case <-grace.C:
		d.logger.Warn("shutdown budget exhausted; cancelling running operations")
		cancelExec()
		<-finished
	}
	stopOutbox()
	<-outboxDone
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelFlush()
	d.publishPending(flushCtx)
	return nil
}

// Ready is closed once recovery has finished and Handle accepts commands.
// Consumers must not pull commands before then.
func (d *Dispatcher) Ready() <-chan struct{} { return d.ready }

// Stats returns how many operations wait for a worker and how many run now.
func (d *Dispatcher) Stats() (queued, running int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.queue), len(d.running)
}

func (d *Dispatcher) setAccepting(value bool) {
	d.mu.Lock()
	d.accepting = value
	d.mu.Unlock()
}

// recover converts operations interrupted by a crash and requeues the rest.
func (d *Dispatcher) recover() error {
	now := d.opts.Now().UTC()
	var requeue []string
	err := d.store.Update(func(doc *state.Document) error {
		for _, op := range doc.Operations {
			switch op.Phase {
			case state.PhaseRunning:
				d.finish(op, agentv1.StatusUnknown, agentv1.CodeAgentRestarted, nil, now)
			case state.PhaseQueued:
				if op.CancelRequested {
					d.finish(op, agentv1.StatusCancelled, agentv1.CodeCancelled, nil, now)
				} else {
					requeue = append(requeue, op.OperationID)
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(requeue) > cap(d.queue) {
		// Cannot happen while QueueSize is unchanged; if it was lowered, the
		// excess is rejected rather than crash-looping the agent.
		excess := requeue[cap(d.queue):]
		requeue = requeue[:cap(d.queue)]
		if err := d.store.Update(func(doc *state.Document) error {
			for _, id := range excess {
				d.finish(doc.Operations[id], agentv1.StatusRejected, agentv1.CodeQueueFull, nil, now)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	for _, id := range requeue {
		d.queue <- id
	}
	d.notifyOutbox()
	return nil
}

// Handle applies one verified command. It returns nil when the command was
// durably processed (or is a duplicate) and may be acknowledged; any error
// means it must be redelivered.
func (d *Dispatcher) Handle(ctx context.Context, cmd agentv1.Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	accepting := d.accepting
	d.mu.Unlock()
	if !accepting {
		return ErrNotAccepting
	}
	if cmd.AgentID != d.opts.AgentID {
		return errors.New("command addressed to another agent")
	}
	now := d.opts.Now().UTC()
	var enqueue string
	var cancelRunning bool
	var notice *agentv1.Result
	err := d.store.Update(func(doc *state.Document) error {
		enqueue, cancelRunning, notice = "", false, nil
		if _, seen := doc.SeenCommands[cmd.CommandID]; seen {
			return errDuplicate
		}
		doc.SeenCommands[cmd.CommandID] = cmd.ExpiresAt
		op := doc.Operations[cmd.OperationID]
		switch cmd.Type {
		case agentv1.CommandOperationRequest:
			if op != nil {
				if op.Capability != cmd.Capability || string(op.Input) != string(cmd.Input) {
					d.logger.Warn("operation ID reused with different content; ignoring", "operation_id", cmd.OperationID)
				}
				return nil // idempotent redelivery; the outbox republishes any result
			}
			op = &state.Operation{
				OperationID: cmd.OperationID, Capability: cmd.Capability, Input: cmd.Input,
				TimeoutMillis: cmd.TimeoutMillis, ExpiresAt: cmd.ExpiresAt, Phase: state.PhaseQueued, AcceptedAt: now,
			}
			doc.Operations[op.OperationID] = op
			if _, ok := d.registry.Lookup(cmd.Capability); !ok {
				d.finish(op, agentv1.StatusRejected, agentv1.CodeCapabilityUnavailable, nil, now)
				return nil
			}
			if pending(doc) > cap(d.queue) {
				d.finish(op, agentv1.StatusRejected, agentv1.CodeQueueFull, nil, now)
				return nil
			}
			enqueue = op.OperationID
			notice = &agentv1.Result{ProtocolVersion: agentv1.ProtocolVersion, AgentID: d.opts.AgentID, OperationID: op.OperationID,
				Capability: op.Capability, Status: agentv1.StatusAccepted, AcceptedAt: now}
		case agentv1.CommandOperationCancel:
			if op == nil || op.Phase == state.PhaseDone {
				return nil
			}
			op.CancelRequested = true
			if op.Phase == state.PhaseQueued {
				d.finish(op, agentv1.StatusCancelled, agentv1.CodeCancelled, nil, now)
			} else {
				cancelRunning = true
			}
		case agentv1.CommandResultAck:
			if op != nil && op.Phase == state.PhaseDone {
				op.ResultAcked = true
				op.Result.Output = nil // no longer needed; keeps the state file small
			}
		}
		return nil
	})
	if errors.Is(err, errDuplicate) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("persist command %s: %w", cmd.CommandID, err)
	}
	if enqueue != "" {
		d.queue <- enqueue // capacity is guaranteed by the pending() check above
	}
	if cancelRunning {
		d.mu.Lock()
		if exec := d.running[cmd.OperationID]; exec != nil {
			exec.cancelledByOp = true
			exec.cancel()
		}
		d.mu.Unlock()
	}
	if notice != nil {
		if err := d.publisher.PublishResult(ctx, *notice); err != nil {
			d.logger.Debug("acceptance notice not delivered; terminal result is durable", "operation_id", notice.OperationID, "error", err)
		}
	}
	d.notifyOutbox()
	return nil
}

var errDuplicate = errors.New("duplicate command")

// pending counts operations that are queued or running.
func pending(doc *state.Document) int {
	n := 0
	for _, op := range doc.Operations {
		if op.Phase != state.PhaseDone {
			n++
		}
	}
	return n
}

func (d *Dispatcher) finish(op *state.Operation, status agentv1.OperationStatus, code string, output json.RawMessage, now time.Time) {
	completed := now
	op.Phase = state.PhaseDone
	op.Input = nil
	op.Result = &agentv1.Result{
		ProtocolVersion: agentv1.ProtocolVersion, AgentID: d.opts.AgentID, OperationID: op.OperationID,
		Capability: op.Capability, Status: status, Code: code, Output: output,
		AcceptedAt: op.AcceptedAt, StartedAt: op.StartedAt, CompletedAt: &completed,
	}
}

func (d *Dispatcher) worker(execCtx context.Context, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case id := <-d.queue:
			select {
			case <-stop:
				return // leave it queued; it is recovered on next start
			default:
			}
			d.execute(execCtx, id)
		}
	}
}

func (d *Dispatcher) execute(execCtx context.Context, id string) {
	now := d.opts.Now().UTC()
	// Register before the running transition: a cancel command that observes
	// "running" in the store is then guaranteed to find this execution. The
	// store serializes the two updates, so no interleaving loses the cancel.
	ctx, cancel := context.WithCancel(execCtx)
	defer cancel()
	exec := &execution{cancel: cancel}
	d.mu.Lock()
	d.running[id] = exec
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.running, id)
		d.mu.Unlock()
	}()

	var op state.Operation
	var start bool
	err := d.store.Update(func(doc *state.Document) error {
		current := doc.Operations[id]
		if current == nil || current.Phase != state.PhaseQueued {
			return nil
		}
		if !current.ExpiresAt.After(now) {
			d.finish(current, agentv1.StatusRejected, agentv1.CodeExpired, nil, now)
			return nil
		}
		started := now
		current.Phase, current.StartedAt = state.PhaseRunning, &started
		op, start = *current, true
		return nil
	})
	if err != nil {
		d.logger.Error("cannot record operation start; leaving it queued", "operation_id", id, "error", err)
		return
	}
	if !start {
		d.notifyOutbox()
		return
	}
	capability, _ := d.registry.Lookup(op.Capability)
	timeout := min(time.Duration(op.TimeoutMillis)*time.Millisecond, d.opts.MaxOperationTimeout)
	runCtx, cancelRun := context.WithTimeout(ctx, timeout)
	output, runErr := safeExecute(runCtx, capability, op.Input)
	deadlineHit := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	cancelRun()
	status, code := agentv1.StatusSucceeded, ""
	d.mu.Lock()
	cancelledByOp := exec.cancelledByOp
	d.mu.Unlock()
	switch {
	case cancelledByOp:
		status, code, output = agentv1.StatusCancelled, agentv1.CodeCancelled, nil
	case deadlineHit:
		status, code, output = agentv1.StatusTimedOut, agentv1.CodeTimedOut, nil
	case execCtx.Err() != nil:
		status, code, output = agentv1.StatusUnknown, agentv1.CodeAgentShutdown, nil
	case capabilities.IsInvalidInput(runErr):
		status, code, output = agentv1.StatusRejected, agentv1.CodeInvalidInput, nil
	case runErr != nil:
		d.logger.Warn("operation failed", "operation_id", id, "capability", op.Capability, "error", runErr)
		status, code, output = agentv1.StatusFailed, agentv1.CodeExecutionFailed, nil
	case len(output) > d.opts.MaxOutputBytes:
		status, code, output = agentv1.StatusFailed, agentv1.CodeOutputTooLarge, nil
	}
	if err := d.store.Update(func(doc *state.Document) error {
		if current := doc.Operations[id]; current != nil && current.Phase == state.PhaseRunning {
			d.finish(current, status, code, output, d.opts.Now().UTC())
		}
		return nil
	}); err != nil {
		// The operation stays "running" on disk and becomes "unknown" on the
		// next start, which is the truthful outcome when we cannot persist.
		d.logger.Error("cannot persist operation result", "operation_id", id, "error", err)
	}
	d.notifyOutbox()
}

// safeExecute isolates capability panics so one faulty capability cannot
// terminate the agent.
func safeExecute(ctx context.Context, capability capabilities.Capability, input json.RawMessage) (output json.RawMessage, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("capability panicked: %v\n%s", recovered, debug.Stack())
		}
	}()
	return capability.Execute(ctx, input)
}

func (d *Dispatcher) notifyOutbox() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *Dispatcher) outbox(ctx context.Context) {
	ticker := time.NewTicker(d.opts.RepublishInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-ticker.C:
		}
		d.publishPending(ctx)
	}
}

// publishPending sends every unacknowledged terminal result. Delivery is
// at-least-once; the controller must upsert by operation ID.
func (d *Dispatcher) publishPending(ctx context.Context) {
	doc := d.store.Snapshot()
	for _, op := range doc.Operations {
		if op.Result == nil || op.ResultAcked {
			continue
		}
		if err := d.publisher.PublishResult(ctx, *op.Result); err != nil {
			if ctx.Err() == nil {
				d.logger.Warn("result publish failed; will retry", "operation_id", op.OperationID, "error", err)
			}
			return
		}
	}
}
