package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/capabilities"
	agentconfig "github.com/machinarchy/machinarchy-agent/internal/config"
	"github.com/machinarchy/machinarchy-agent/internal/events"
	"github.com/machinarchy/machinarchy-agent/internal/identity"
	agentv1 "github.com/machinarchy/machinarchy-agent/protocol/agent/v1"
)

const (
	operationProtocolVersion     = agentv1.ProtocolVersion
	maxOperationHistory          = 4096
	operationForcedShutdownGrace = 2 * time.Second
)

// Operation wire types are aliases to the shared versioned event contract.
type OperationRequestPayload = agentv1.OperationRequest
type OperationCancellationPayload = agentv1.OperationCancellation
type OperationStatus = agentv1.OperationStatus
type OperationResult = agentv1.OperationResult

const (
	OperationStatusAccepted      = agentv1.OperationStatusAccepted
	OperationStatusRunning       = agentv1.OperationStatusRunning
	OperationStatusCancelRequest = agentv1.OperationStatusCancelRequest
	OperationStatusSucceeded     = agentv1.OperationStatusSucceeded
	OperationStatusFailed        = agentv1.OperationStatusFailed
	OperationStatusCancelled     = agentv1.OperationStatusCancelled
	OperationStatusTimedOut      = agentv1.OperationStatusTimedOut
	OperationStatusRejected      = agentv1.OperationStatusRejected
	OperationStatusUnknown       = agentv1.OperationStatusUnknown
)

// MessagePublisher is the minimal outbound transport capability required to
// publish an agent operation result.
type MessagePublisher interface {
	Publish(context.Context, string, []byte) error
}

type OperationResultPublisher interface {
	PublishOperationResult(context.Context, OperationResult) error
}

type NATSOperationResultPublisher struct {
	publisher MessagePublisher
}

func NewNATSOperationResultPublisher(publisher MessagePublisher) (*NATSOperationResultPublisher, error) {
	if publisher == nil {
		return nil, errors.New("operation result message publisher is required")
	}
	return &NATSOperationResultPublisher{publisher: publisher}, nil
}

func (p *NATSOperationResultPublisher) PublishOperationResult(ctx context.Context, result OperationResult) error {
	if ctx == nil {
		return errors.New("operation result publish context is required")
	}
	subject, err := AgentOperationResultsSubject(result.AgentID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode operation result: %w", err)
	}
	if len(payload) > agentconfig.MaxOperationOutputBytes+16*1024 {
		return errors.New("operation result exceeds the maximum message size")
	}
	return p.publisher.Publish(ctx, subject, payload)
}

func AgentOperationResultsSubject(agentID string) (string, error) {
	return agentv1.AgentOperationResultsSubject(agentID)
}

type OperationDispatcherConfig struct {
	Workers              int
	QueueSize            int
	MaxOperationTimeout  time.Duration
	MaxInputBytes        int
	MaxOutputBytes       int
	ResultPublishTimeout time.Duration
}

type operationRecord struct {
	status OperationStatus
	cancel context.CancelFunc
}

type queuedOperation struct {
	request    OperationRequestPayload
	event      events.AgentEventEnvelope
	capability capabilities.Capability
	ctx        context.Context
	cancel     context.CancelFunc
	acceptedAt time.Time
}

// OperationDispatcher is both the Phase 3 event handler and a bounded worker
// pool. It never interprets an operation as a shell command.
type OperationDispatcher struct {
	identity  identity.AgentIdentity
	registry  *capabilities.Registry
	publisher OperationResultPublisher
	store     OperationStore
	config    OperationDispatcherConfig
	logger    *slog.Logger

	mu           sync.Mutex
	publishMu    sync.Mutex
	outboxMu     sync.Mutex
	started      bool
	accepting    bool
	stopping     bool
	queue        chan *queuedOperation
	slots        chan struct{}
	records      map[string]operationRecord
	queued       map[string]bool
	order        []string
	ctx          context.Context
	cancel       context.CancelFunc
	resultCtx    context.Context
	resultCancel context.CancelFunc
	workers      sync.WaitGroup
	done         chan struct{}
	outboxDone   chan struct{}
}

func NewOperationDispatcher(identity identity.AgentIdentity, registry *capabilities.Registry, publisher OperationResultPublisher, config OperationDispatcherConfig, logger *slog.Logger) (*OperationDispatcher, error) {
	return NewOperationDispatcherWithStore(identity, registry, publisher, NewMemoryOperationStore(), config, logger)
}

func NewOperationDispatcherWithStore(identity identity.AgentIdentity, registry *capabilities.Registry, publisher OperationResultPublisher, store OperationStore, config OperationDispatcherConfig, logger *slog.Logger) (*OperationDispatcher, error) {
	if err := identity.Validate(); err != nil || !identity.Registered() {
		return nil, errors.New("registered agent identity is required for operation dispatch")
	}
	if registry == nil {
		return nil, errors.New("capability registry is required")
	}
	if publisher == nil {
		return nil, errors.New("operation result publisher is required")
	}
	if store == nil {
		return nil, errors.New("durable operation store is required")
	}
	if config.Workers < 1 || config.Workers > agentconfig.MaxOperationWorkers || config.QueueSize < 1 || config.QueueSize > agentconfig.MaxOperationQueue {
		return nil, errors.New("operation workers or queue size are outside supported limits")
	}
	if config.MaxOperationTimeout <= 0 || config.MaxOperationTimeout > agentconfig.MaxOperationTimeout || config.MaxInputBytes < 1 || config.MaxInputBytes > agentconfig.MaxOperationInputBytes || config.MaxOutputBytes < 1 || config.MaxOutputBytes > agentconfig.MaxOperationOutputBytes || config.ResultPublishTimeout <= 0 {
		return nil, errors.New("operation timeout, payload limits, or result publish timeout are invalid")
	}
	if logger == nil {
		logger = slog.Default()
	}
	registry.Seal()
	return &OperationDispatcher{
		identity: identity, registry: registry, publisher: publisher, store: store, config: config,
		logger: logger.With("component", "agent-operation-dispatcher"),
		queue:  make(chan *queuedOperation, config.QueueSize), slots: make(chan struct{}, config.QueueSize),
		records: make(map[string]operationRecord), queued: make(map[string]bool), done: make(chan struct{}), outboxDone: make(chan struct{}),
	}, nil
}

func (d *OperationDispatcher) Start(parent context.Context) error {
	if parent == nil {
		return errors.New("operation dispatcher context is required")
	}
	if err := parent.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return errors.New("operation dispatcher has already started")
	}
	d.mu.Unlock()

	entries, err := d.store.List(parent)
	if err != nil {
		return fmt.Errorf("load durable operation history: %w", err)
	}
	runtimeCtx, runtimeCancel := context.WithCancel(context.WithoutCancel(parent))
	resultCtx, resultCancel := context.WithCancel(context.Background())
	cleanupOnError := true
	defer func() {
		if cleanupOnError {
			runtimeCancel()
			resultCancel()
		}
	}()

	recovered := make([]*queuedOperation, 0)
	recoveredEntries := make(map[string]StoredOperation, len(entries))
	for _, entry := range entries {
		switch entry.Status {
		case OperationStatusAccepted:
			if !entry.Event.ExpiresAt.After(time.Now()) {
				result := d.result(entry.Event, entry.OperationID, entry.Capability, OperationStatusRejected, "operation_expired_before_execution", nil)
				result.AcceptedAt, result.CompletedAt = entry.AcceptedAt, time.Now().UTC()
				if err := d.store.Complete(parent, entry.OperationID, result); err != nil {
					return fmt.Errorf("persist expired recovered operation: %w", err)
				}
				entry.Status, entry.Result = OperationStatusRejected, &result
			} else {
				op, recoverErr := d.recoverQueuedOperation(runtimeCtx, entry)
				if recoverErr != nil {
					result := d.result(entry.Event, entry.OperationID, entry.Capability, OperationStatusRejected, "capability_unavailable_after_restart", nil)
					result.AcceptedAt, result.CompletedAt = entry.AcceptedAt, time.Now().UTC()
					if storeErr := d.store.Complete(parent, entry.OperationID, result); storeErr != nil {
						return fmt.Errorf("persist rejected recovered operation: %w", storeErr)
					}
					entry.Status, entry.Result = OperationStatusRejected, &result
				} else {
					recovered = append(recovered, op)
				}
			}
		case OperationStatusCancelled:
			// A cancellation received before the operation was accepted can be
			// persisted as a tombstone before its result. Repair that narrow crash
			// window on restart instead of silently losing the cancellation result.
			if entry.Result == nil && !entry.ResultAcknowledged {
				result := d.result(entry.Event, entry.OperationID, entry.Capability, OperationStatusCancelled, "cancelled_before_execution", nil)
				result.AcceptedAt, result.CompletedAt = entry.AcceptedAt, time.Now().UTC()
				if err := d.store.Complete(parent, entry.OperationID, result); err != nil {
					return fmt.Errorf("persist recovered cancellation result: %w", err)
				}
				entry.Result = &result
				entry.CompletedAt = result.CompletedAt
			}
		case OperationStatusCancelRequest:
			if entry.StartedAt.IsZero() {
				// A queued operation cancelled before the durable running transition
				// cannot have performed a side effect; recover it as cancelled.
				result := d.result(entry.Event, entry.OperationID, entry.Capability, OperationStatusCancelled, "cancelled_before_execution", nil)
				result.AcceptedAt, result.CompletedAt = entry.AcceptedAt, time.Now().UTC()
				if err := d.store.Complete(parent, entry.OperationID, result); err != nil {
					return fmt.Errorf("persist recovered pre-execution cancellation: %w", err)
				}
				entry.Status, entry.Result, entry.CompletedAt = OperationStatusCancelled, &result, result.CompletedAt
			} else {
				// The cancellation raced an operation that had started. Its side
				// effect may have completed, so never replay it automatically.
				result := d.result(entry.Event, entry.OperationID, entry.Capability, OperationStatusUnknown, "agent_restarted_during_execution", nil)
				result.AcceptedAt, result.StartedAt, result.CompletedAt = entry.AcceptedAt, entry.StartedAt, time.Now().UTC()
				if err := d.store.Complete(parent, entry.OperationID, result); err != nil {
					return fmt.Errorf("persist unknown recovered operation: %w", err)
				}
				entry.Status, entry.Result = OperationStatusUnknown, &result
				entry.CompletedAt = result.CompletedAt
			}
		case OperationStatusRunning:
			// The Agent cannot know whether an external side effect completed.
			// Preserve that uncertainty; never automatically launch it a second time.
			result := d.result(entry.Event, entry.OperationID, entry.Capability, OperationStatusUnknown, "agent_restarted_during_execution", nil)
			result.AcceptedAt, result.StartedAt, result.CompletedAt = entry.AcceptedAt, entry.StartedAt, time.Now().UTC()
			if err := d.store.Complete(parent, entry.OperationID, result); err != nil {
				return fmt.Errorf("persist unknown recovered operation: %w", err)
			}
			entry.Status, entry.Result = OperationStatusUnknown, &result
			entry.CompletedAt = result.CompletedAt
		}
		recoveredEntries[entry.OperationID] = entry
	}
	if len(recovered) > d.config.QueueSize {
		return fmt.Errorf("durable accepted operation backlog (%d) exceeds configured queue capacity (%d); increase MACHINARCHY_AGENT_OPERATION_QUEUE_SIZE before restarting", len(recovered), d.config.QueueSize)
	}

	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return errors.New("operation dispatcher has already started")
	}
	d.ctx, d.cancel = runtimeCtx, runtimeCancel
	d.resultCtx, d.resultCancel = resultCtx, resultCancel
	d.started = true
	d.accepting = true
	for _, entry := range entries {
		current := recoveredEntries[entry.OperationID]
		d.records[entry.OperationID] = operationRecord{status: current.Status}
		d.order = append(d.order, entry.OperationID)
	}
	for _, op := range recovered {
		d.slots <- struct{}{}
		d.queue <- op
		d.queued[op.request.OperationID] = true
	}
	for range d.config.Workers {
		d.workers.Add(1)
		go d.worker()
	}
	go func() {
		d.workers.Wait()
		close(d.done)
	}()
	go func() {
		defer close(d.outboxDone)
		d.resultOutboxLoop()
	}()
	d.mu.Unlock()
	cleanupOnError = false
	return nil
}

func (d *OperationDispatcher) recoverQueuedOperation(parent context.Context, entry StoredOperation) (*queuedOperation, error) {
	capability, ok := d.registry.Lookup(entry.Capability)
	if !ok {
		return nil, fmt.Errorf("capability %q is no longer registered", entry.Capability)
	}
	remaining := time.Until(entry.Event.ExpiresAt)
	if remaining <= 0 {
		return nil, errors.New("accepted operation has expired")
	}
	timeout := remaining
	if entry.Request.TimeoutMilli > 0 && time.Duration(entry.Request.TimeoutMilli)*time.Millisecond < timeout {
		timeout = time.Duration(entry.Request.TimeoutMilli) * time.Millisecond
	}
	if timeout > d.config.MaxOperationTimeout {
		timeout = d.config.MaxOperationTimeout
	}
	request := entry.Request
	if request.TimeoutMilli <= 0 {
		request.TimeoutMilli = timeout.Milliseconds()
	}
	outputLimit := int(request.OutputLimitBytes)
	if outputLimit < 1 || outputLimit > d.config.MaxOutputBytes {
		outputLimit = d.config.MaxOutputBytes
		request.OutputLimitBytes = int64(outputLimit)
	}
	opCtx, cancel := context.WithTimeout(parent, timeout)
	return &queuedOperation{request: request, event: entry.Event, capability: capability, ctx: opCtx, cancel: cancel, acceptedAt: entry.AcceptedAt}, nil
}

func (d *OperationDispatcher) Handle(ctx context.Context, event events.AgentEventEnvelope) error {
	if ctx == nil {
		return errors.New("operation event context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch event.EventType {
	case events.AgentEventOperationRequested:
		return d.handleRequest(ctx, event)
	case events.AgentEventOperationCancelled:
		return d.handleCancellation(ctx, event)
	case events.AgentEventOperationResultAcknowledged:
		return d.handleResultAcknowledgement(ctx, event)
	default:
		return events.NewInvalidAgentEventError("unsupported operation event type")
	}
}

func (d *OperationDispatcher) handleRequest(ctx context.Context, event events.AgentEventEnvelope) error {
	request, err := decodeOperationRequest(event.Payload, d.config.MaxInputBytes)
	if err != nil {
		return err
	}
	capability, capabilityAvailable := d.registry.Lookup(request.Capability)
	if request.TimeoutMilli == 0 {
		request.TimeoutMilli = agentconfig.MaxOperationTimeout.Milliseconds()
	}
	timeout := time.Duration(request.TimeoutMilli) * time.Millisecond
	if timeout <= 0 {
		return events.NewInvalidAgentEventError("operation timeout must be positive")
	}
	if timeout > d.config.MaxOperationTimeout {
		timeout = d.config.MaxOperationTimeout
	}
	remaining := time.Until(event.ExpiresAt)
	if remaining <= 0 {
		return events.NewInvalidAgentEventError("operation event expired before dispatch")
	}
	if timeout > remaining {
		timeout = remaining
	}
	if request.OutputLimitBytes == 0 {
		request.OutputLimitBytes = int64(d.config.MaxOutputBytes)
	}
	if request.OutputLimitBytes < 1 || request.OutputLimitBytes > int64(d.config.MaxOutputBytes) {
		return events.NewInvalidAgentEventError("operation output limit is outside the locally configured range")
	}

	d.mu.Lock()
	if !d.started || !d.accepting {
		d.mu.Unlock()
		return errors.New("operation dispatcher is not accepting work")
	}
	stored, exists, err := d.store.Get(ctx, request.OperationID)
	if err != nil {
		d.mu.Unlock()
		return fmt.Errorf("inspect durable operation record: %w", err)
	}
	if exists {
		if stored.Status == OperationStatusCancelled {
			if stored.Result == nil && stored.ResultAcknowledged {
				// A duplicate request may arrive after the controller has already
				// acknowledged the cancellation result. The durable acknowledgement
				// is authoritative; do not recreate and republish the result.
				d.records[request.OperationID] = operationRecord{status: OperationStatusCancelled}
				d.mu.Unlock()
				return nil
			}
			result := d.result(event, request.OperationID, request.Capability, OperationStatusCancelled, "cancelled_before_execution", nil)
			result.AcceptedAt, result.CompletedAt = stored.AcceptedAt, time.Now().UTC()
			if stored.Result == nil {
				if err := d.store.Complete(ctx, request.OperationID, result); err != nil {
					d.mu.Unlock()
					return fmt.Errorf("persist cancellation result: %w", err)
				}
			}
			d.records[request.OperationID] = operationRecord{status: OperationStatusCancelled}
			d.mu.Unlock()
			if err := d.publishPendingResults(ctx); err != nil {
				d.logger.Warn("cancelled operation result remains in durable outbox", "operation_id", request.OperationID, "error", err)
			}
			return nil
		}
		if !sameStoredOperationRequest(stored, request) {
			d.mu.Unlock()
			return events.NewInvalidAgentEventError("operation ID was reused with different content")
		}
		if stored.Status == OperationStatusAccepted && !d.queued[request.OperationID] {
			select {
			case d.slots <- struct{}{}:
			default:
				d.mu.Unlock()
				return errors.New("previously accepted operation could not be requeued because the local queue is full; restart the Agent to recover persisted accepted work")
			}
			op, recoverErr := d.recoverQueuedOperation(d.ctx, stored)
			if recoverErr != nil {
				<-d.slots
				d.mu.Unlock()
				return fmt.Errorf("requeue accepted operation: %w", recoverErr)
			}
			d.queue <- op
			d.queued[request.OperationID] = true
			d.records[request.OperationID] = operationRecord{status: OperationStatusAccepted, cancel: op.cancel}
		}
		status := stored.Status
		acceptedAt := stored.AcceptedAt
		d.mu.Unlock()
		if status == OperationStatusAccepted {
			accepted := d.result(event, request.OperationID, request.Capability, OperationStatusAccepted, "", nil)
			accepted.AcceptedAt = acceptedAt
			if err := d.publishResult(ctx, accepted); err != nil {
				// Acceptance is already durable and the terminal result will use the
				// durable outbox. A transient progress-notification failure must not
				// tear down the whole Agent session.
				d.logger.Warn("could not publish duplicate operation acceptance; durable operation remains active", "operation_id", request.OperationID, "error", err)
			}
			return nil
		}
		d.logger.Debug("duplicate operation request suppressed by durable operation ID", "operation_id", request.OperationID, "status", status)
		return nil
	}
	if !capabilityAvailable {
		// Persist unsupported-capability outcomes as well, so a redelivery cannot
		// accidentally change the result after an Agent upgrade.
		entry := StoredOperation{OperationID: request.OperationID, Capability: request.Capability, Request: request, Event: event, Status: OperationStatusAccepted, AcceptedAt: time.Now().UTC()}
		_, created, storeErr := d.store.Accept(ctx, entry)
		if storeErr != nil {
			d.mu.Unlock()
			return fmt.Errorf("persist rejected operation: %w", storeErr)
		}
		if created {
			result := d.result(event, request.OperationID, request.Capability, OperationStatusRejected, "capability_unavailable", nil)
			result.AcceptedAt, result.CompletedAt = entry.AcceptedAt, time.Now().UTC()
			if err := d.store.Complete(ctx, request.OperationID, result); err != nil {
				d.mu.Unlock()
				return fmt.Errorf("persist unsupported capability result: %w", err)
			}
			d.records[request.OperationID] = operationRecord{status: OperationStatusRejected}
			d.rememberLocked(request.OperationID)
		}
		d.mu.Unlock()
		if err := d.publishPendingResults(ctx); err != nil {
			d.logger.Warn("rejected capability result remains in durable outbox", "operation_id", request.OperationID, "error", err)
		}
		return nil
	}
	select {
	case d.slots <- struct{}{}:
	default:
		d.mu.Unlock()
		return d.publishSimpleResult(ctx, event, request, OperationStatusRejected, "operation_queue_full")
	}
	opCtx, cancel := context.WithTimeout(d.ctx, timeout)
	acceptedAt := time.Now().UTC()
	op := &queuedOperation{request: request, event: event, capability: capability, ctx: opCtx, cancel: cancel, acceptedAt: acceptedAt}
	entry := StoredOperation{OperationID: request.OperationID, Capability: request.Capability, Request: request, Event: event, Status: OperationStatusAccepted, AcceptedAt: acceptedAt}
	_, created, err := d.store.Accept(ctx, entry)
	if err != nil || !created {
		cancel()
		<-d.slots
		d.mu.Unlock()
		if err != nil {
			return fmt.Errorf("persist operation acceptance: %w", err)
		}
		return errors.New("operation appeared concurrently during acceptance")
	}
	d.records[request.OperationID] = operationRecord{status: OperationStatusAccepted, cancel: cancel}
	d.rememberLocked(request.OperationID)
	d.queued[request.OperationID] = true
	d.queue <- op // Capacity was reserved and durable acceptance completed first.
	d.mu.Unlock()

	accepted := d.result(event, request.OperationID, request.Capability, OperationStatusAccepted, "", nil)
	accepted.AcceptedAt = acceptedAt
	if err := d.publishResult(ctx, accepted); err != nil {
		// The operation remains durably accepted and queued. The terminal result
		// is persisted and retried through the outbox, so this progress notice is
		// best-effort and must not tear down the Agent session.
		d.logger.Warn("operation acceptance notification failed; operation remains durably queued", "operation_id", request.OperationID, "error", err)
	}
	return nil
}

func (d *OperationDispatcher) handleCancellation(ctx context.Context, event events.AgentEventEnvelope) error {
	var request OperationCancellationPayload
	if err := decodeStrictObject(event.Payload, &request, 1024); err != nil {
		return events.NewInvalidAgentEventError("operation cancellation payload is invalid")
	}
	if !events.ValidEventIdentifier(request.OperationID) || len(request.Reason) > 256 || strings.ContainsAny(request.Reason, "\r\n\x00") {
		return events.NewInvalidAgentEventError("operation cancellation fields are invalid")
	}

	// Serialize the durable cancellation transition with the worker's durable
	// accepted-to-running transition. Otherwise cancellation can persist first,
	// make MarkRunning fail, and leave an acknowledged operation stranded.
	d.mu.Lock()
	stored, storedExists, err := d.store.Get(ctx, request.OperationID)
	if err != nil {
		d.mu.Unlock()
		return fmt.Errorf("inspect operation before cancellation: %w", err)
	}
	if storedExists && isTerminalOperationStatus(stored.Status) &&
		!(stored.Status == OperationStatusCancelled && stored.Result == nil && !stored.ResultAcknowledged) {
		d.mu.Unlock()
		d.logger.Debug("ignoring cancellation for an already terminal operation", "operation_id", request.OperationID, "status", stored.Status)
		return nil
	}
	if err := d.store.MarkCancellation(ctx, request.OperationID, event); err != nil {
		d.mu.Unlock()
		return fmt.Errorf("persist operation cancellation: %w", err)
	}
	record, exists := d.records[request.OperationID]
	if !exists {
		result := d.result(event, request.OperationID, "", OperationStatusCancelled, "cancelled_before_execution", nil)
		result.CompletedAt = time.Now().UTC()
		if err := d.store.Complete(ctx, request.OperationID, result); err != nil {
			d.mu.Unlock()
			return fmt.Errorf("persist cancellation tombstone result: %w", err)
		}
		d.records[request.OperationID] = operationRecord{status: OperationStatusCancelled}
		d.rememberLocked(request.OperationID)
		d.mu.Unlock()
		if err := d.publishPendingResults(ctx); err != nil {
			d.logger.Warn("pre-execution cancellation result remains in durable outbox", "operation_id", request.OperationID, "error", err)
		}
		return nil
	}
	if record.status == OperationStatusAccepted || record.status == OperationStatusRunning || record.status == OperationStatusCancelRequest {
		if record.cancel != nil {
			record.cancel()
		}
		// Keep the record non-terminal until the worker confirms it has exited.
		d.records[request.OperationID] = operationRecord{status: OperationStatusCancelRequest, cancel: record.cancel}
	}
	d.mu.Unlock()
	d.logger.Info("operation cancellation requested", "operation_id", request.OperationID)
	return nil
}

func (d *OperationDispatcher) handleResultAcknowledgement(ctx context.Context, event events.AgentEventEnvelope) error {
	var acknowledgement agentv1.OperationResultAcknowledgement
	if err := decodeStrictObject(event.Payload, &acknowledgement, 1024); err != nil || !events.ValidEventIdentifier(acknowledgement.OperationID) {
		return events.NewInvalidAgentEventError("operation result acknowledgement is invalid")
	}
	if err := d.store.AcknowledgeResult(ctx, acknowledgement.OperationID); err != nil {
		// A duplicate acknowledgement is harmless after its result was already
		// removed; treat that case as idempotent.
		if strings.Contains(err.Error(), "not pending acknowledgement") || strings.Contains(err.Error(), "does not exist") {
			return nil
		}
		return fmt.Errorf("acknowledge durable operation result: %w", err)
	}
	d.logger.Debug("controller acknowledged operation result", "operation_id", acknowledgement.OperationID)
	return nil
}

func sameStoredOperationRequest(stored StoredOperation, request OperationRequestPayload) bool {
	digest := operationRequestDigest(request)
	if stored.RequestDigest != "" {
		return stored.RequestDigest == digest
	}
	return stored.Capability == request.Capability && bytes.Equal(stored.Request.Input, request.Input)
}

func decodeOperationRequest(payload json.RawMessage, maxInput int) (OperationRequestPayload, error) {
	var request OperationRequestPayload
	if err := decodeStrictObject(payload, &request, agentconfig.MaxOperationInputBytes+4096); err != nil {
		return OperationRequestPayload{}, events.NewInvalidAgentEventError("operation request payload is invalid")
	}
	if !events.ValidEventIdentifier(request.OperationID) || !capabilities.ValidName(request.Capability) {
		return OperationRequestPayload{}, events.NewInvalidAgentEventError("operation identifier or capability name is invalid")
	}
	if len(request.Input) == 0 || len(request.Input) > maxInput {
		return OperationRequestPayload{}, events.NewInvalidAgentEventError("operation input is empty or exceeds the configured limit")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(request.Input, &object); err != nil || object == nil {
		return OperationRequestPayload{}, events.NewInvalidAgentEventError("operation input must be a JSON object")
	}
	if request.TimeoutMilli < 0 || request.TimeoutMilli > agentconfig.MaxOperationTimeout.Milliseconds() {
		return OperationRequestPayload{}, events.NewInvalidAgentEventError("operation timeout is outside the supported range")
	}
	if request.OutputLimitBytes < 0 || request.OutputLimitBytes > agentconfig.MaxOperationOutputBytes {
		return OperationRequestPayload{}, events.NewInvalidAgentEventError("operation output limit is outside the supported range")
	}
	return request, nil
}

func decodeStrictObject(payload []byte, target any, limit int) error {
	if len(payload) == 0 || len(payload) > limit {
		return errors.New("payload size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func (d *OperationDispatcher) worker() {
	defer d.workers.Done()
	for op := range d.queue {
		<-d.slots // Release queue capacity once a worker has accepted the item.
		d.execute(op)
	}
}

func (d *OperationDispatcher) execute(op *queuedOperation) {
	started := time.Now().UTC()
	d.mu.Lock()
	record := d.records[op.request.OperationID]
	if op.ctx.Err() != nil || record.status == OperationStatusCancelRequest || record.status == OperationStatusCancelled {
		d.records[op.request.OperationID] = operationRecord{status: OperationStatusCancelled}
		d.mu.Unlock()
		d.complete(op, OperationStatusCancelled, "operation_cancelled", nil, started)
		return
	}
	// Persist the running transition while holding the dispatcher mutex so a
	// concurrent cancellation cannot win in the durable store between the
	// in-memory check and this write.
	if err := d.store.MarkRunning(context.Background(), op.request.OperationID, started); err != nil {
		delete(d.queued, op.request.OperationID)
		d.records[op.request.OperationID] = operationRecord{status: OperationStatusAccepted}
		d.mu.Unlock()
		d.logger.Error("could not durably mark operation running; refusing to execute until it can be safely recovered", "operation_id", op.request.OperationID, "error", err)
		op.cancel()
		return
	}
	d.records[op.request.OperationID] = operationRecord{status: OperationStatusRunning, cancel: op.cancel}
	delete(d.queued, op.request.OperationID)
	d.mu.Unlock()

	output, err := safeExecuteCapability(op.ctx, op.capability, op.request.Input)
	status := OperationStatusSucceeded
	code := ""
	if err != nil {
		var capabilityErr *capabilities.CapabilityError
		hasExecutionResult := errors.As(err, &capabilityErr)
		if hasExecutionResult {
			output = capabilityErr.Output
		}
		switch {
		case errors.Is(op.ctx.Err(), context.DeadlineExceeded):
			status, code = OperationStatusTimedOut, "operation_timeout"
		case errors.Is(op.ctx.Err(), context.Canceled):
			status, code = OperationStatusCancelled, "operation_cancelled"
		default:
			if errors.Is(err, errCapabilityPanicked) {
				status, code, output = OperationStatusFailed, "capability_panicked", nil
			} else if hasExecutionResult {
				status, code = OperationStatusFailed, capabilityErr.Code
			} else {
				status, code, output = OperationStatusFailed, "capability_execution_failed", nil
			}
		}
		if len(output) > 0 && (!json.Valid(output) || len(output) > int(op.request.OutputLimitBytes)) {
			status, code, output = OperationStatusFailed, "operation_output_exceeded_limit", nil
		}
	} else if len(output) == 0 || !json.Valid(output) {
		status, code, output = OperationStatusFailed, "capability_returned_invalid_json", nil
	} else if len(output) > int(op.request.OutputLimitBytes) {
		status, code, output = OperationStatusFailed, "operation_output_exceeded_limit", nil
	}
	d.complete(op, status, code, output, started)
}

var errCapabilityPanicked = errors.New("capability panicked")

func safeExecuteCapability(ctx context.Context, capability capabilities.Capability, input json.RawMessage) (output json.RawMessage, err error) {
	defer func() {
		if recover() != nil {
			// Do not expose panic values or stack traces: they can contain payloads
			// and other sensitive runtime details. Isolate the failure to this job.
			output = nil
			err = errCapabilityPanicked
		}
	}()
	return capability.Execute(ctx, input)
}

func (d *OperationDispatcher) complete(op *queuedOperation, status OperationStatus, code string, output json.RawMessage, started time.Time) {
	completed := time.Now().UTC()
	op.cancel()
	result := d.result(op.event, op.request.OperationID, op.request.Capability, status, code, output)
	result.AcceptedAt = op.acceptedAt
	result.StartedAt = started
	result.CompletedAt = completed
	result.DurationMillis = completed.Sub(started).Milliseconds()
	d.mu.Lock()
	err := d.store.Complete(context.Background(), op.request.OperationID, result)
	if err == nil {
		d.records[op.request.OperationID] = operationRecord{status: status}
	}
	d.mu.Unlock()
	if err != nil {
		d.logger.Error("could not durably record operation result; result will not be advertised", "operation_id", op.request.OperationID, "status", status, "error", err)
		return
	}
	if err := d.publishPendingResults(context.Background()); err != nil {
		d.logger.Warn("operation result remains in durable outbox", "operation_id", op.request.OperationID, "error", err)
	}
	d.logger.Info("operation completed", "operation_id", op.request.OperationID, "capability", op.request.Capability, "status", status, "duration_ms", result.DurationMillis)
}

func (d *OperationDispatcher) resultOutboxLoop() {
	if err := d.publishPendingResults(d.resultCtx); err != nil && d.resultCtx.Err() == nil {
		d.logger.Warn("initial operation result outbox flush failed", "error", err)
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.resultCtx.Done():
			return
		case <-ticker.C:
			if err := d.publishPendingResults(d.resultCtx); err != nil && d.resultCtx.Err() == nil {
				d.logger.Warn("retrying pending operation result outbox", "error", err)
			}
		}
	}
}

func (d *OperationDispatcher) publishPendingResults(ctx context.Context) error {
	d.outboxMu.Lock()
	defer d.outboxMu.Unlock()
	results, err := d.store.PendingResults(ctx, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("load pending operation results: %w", err)
	}
	var failures []error
	for _, result := range results {
		if err := d.publishResultDetached(result); err != nil {
			failures = append(failures, fmt.Errorf("publish operation %s: %w", result.OperationID, err))
			continue
		}
		if err := d.store.MarkResultPublished(context.Background(), result.OperationID, time.Now().UTC()); err != nil {
			failures = append(failures, fmt.Errorf("record operation %s publish time: %w", result.OperationID, err))
		}
	}
	return errors.Join(failures...)
}

func (d *OperationDispatcher) publishSimpleResult(ctx context.Context, event events.AgentEventEnvelope, request OperationRequestPayload, status OperationStatus, code string) error {
	// Even admission rejections must use the durable result outbox. Otherwise
	// the controller cannot safely acknowledge the corresponding JetStream
	// command: the Agent would have no pending result to retire after a restart.
	acceptedAt := time.Now().UTC()
	entry := StoredOperation{
		OperationID: request.OperationID,
		Capability:  request.Capability,
		Request:     request,
		Event:       event,
		Status:      OperationStatusAccepted,
		AcceptedAt:  acceptedAt,
	}
	stored, created, err := d.store.Accept(ctx, entry)
	if err != nil {
		return fmt.Errorf("persist rejected operation acceptance: %w", err)
	}
	if !created {
		if stored.Status == status && stored.Result != nil {
			return d.publishPendingResults(ctx)
		}
		return errors.New("operation appeared concurrently while rejecting queue-full request")
	}

	result := d.result(event, request.OperationID, request.Capability, status, code, nil)
	result.AcceptedAt = acceptedAt
	result.CompletedAt = time.Now().UTC()
	if err := d.store.Complete(ctx, request.OperationID, result); err != nil {
		return fmt.Errorf("persist rejected operation result: %w", err)
	}
	d.mu.Lock()
	d.records[request.OperationID] = operationRecord{status: status}
	d.rememberLocked(request.OperationID)
	d.mu.Unlock()
	if err := d.publishPendingResults(ctx); err != nil {
		d.logger.Warn("queue-full rejection remains in durable result outbox", "operation_id", request.OperationID, "error", err)
	}
	return nil
}

func (d *OperationDispatcher) result(event events.AgentEventEnvelope, operationID, capability string, status OperationStatus, code string, output json.RawMessage) OperationResult {
	return OperationResult{
		ProtocolVersion: operationProtocolVersion, AgentID: d.identity.AgentID, HostID: d.identity.HostID,
		OperationID: operationID, CorrelationID: event.CorrelationID, Capability: capability,
		Status: status, ErrorCode: code, Output: output,
	}
}

func (d *OperationDispatcher) publishResult(ctx context.Context, result OperationResult) error {
	if ctx == nil {
		return errors.New("operation result publish context is required")
	}
	publishCtx, cancel := context.WithTimeout(ctx, d.config.ResultPublishTimeout)
	defer cancel()
	return d.publishWithRetry(publishCtx, result)
}

func (d *OperationDispatcher) publishResultDetached(result OperationResult) error {
	ctx, cancel := context.WithTimeout(d.resultCtx, d.config.ResultPublishTimeout)
	defer cancel()
	return d.publishWithRetry(ctx, result)
}

func (d *OperationDispatcher) publishWithRetry(ctx context.Context, result OperationResult) error {
	d.publishMu.Lock()
	defer d.publishMu.Unlock()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := d.publisher.PublishOperationResult(ctx, result); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if ctx.Err() != nil {
			return errors.Join(lastErr, ctx.Err())
		}
		if attempt < 2 {
			if err := waitContext(ctx, time.Duration(attempt+1)*200*time.Millisecond); err != nil {
				return errors.Join(lastErr, err)
			}
		}
	}
	return lastErr
}

func (d *OperationDispatcher) rememberLocked(operationID string) {
	d.order = append(d.order, operationID)
	for len(d.records) > maxOperationHistory {
		evicted := false
		for index, candidate := range d.order {
			record, exists := d.records[candidate]
			if !exists {
				d.order = append(d.order[:index], d.order[index+1:]...)
				evicted = true
				break
			}
			if isTerminalOperationStatus(record.status) {
				delete(d.records, candidate)
				d.order = append(d.order[:index], d.order[index+1:]...)
				evicted = true
				break
			}
		}
		if !evicted {
			// All retained operations are active; queue/worker bounds cap further growth.
			break
		}
	}
}

func isTerminalOperationStatus(status OperationStatus) bool {
	switch status {
	case OperationStatusSucceeded, OperationStatusFailed, OperationStatusCancelled, OperationStatusTimedOut, OperationStatusRejected, OperationStatusUnknown:
		return true
	default:
		return false
	}
}

// stopAdmissionLocked closes the queue exactly once. Callers must hold d.mu.
func (d *OperationDispatcher) stopAdmissionLocked() {
	if d.stopping {
		return
	}
	d.accepting = false
	d.stopping = true
	close(d.queue)
}

// cancelActive cancels every queued/running operation context and the worker
// root context. It does not cancel the result context, allowing workers a
// bounded opportunity to publish cancellation outcomes before shutdown ends.
func (d *OperationDispatcher) cancelActive() {
	d.mu.Lock()
	for _, record := range d.records {
		if record.cancel != nil {
			record.cancel()
		}
	}
	rootCancel := d.cancel
	d.mu.Unlock()
	if rootCancel != nil {
		rootCancel()
	}
}

func (d *OperationDispatcher) finishResults() {
	if d.resultCancel != nil {
		d.resultCancel()
	}
	if d.outboxDone != nil {
		<-d.outboxDone
	}
}

// Shutdown stops admission and drains accepted operations. If the drain
// deadline expires, it cancels remaining operations and grants a short,
// bounded cancellation/result-publication grace period. It is safe to call
// more than once.
func (d *OperationDispatcher) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("operation dispatcher shutdown context is required")
	}
	d.mu.Lock()
	if !d.started {
		d.mu.Unlock()
		return nil
	}
	d.stopAdmissionLocked()
	done := d.done
	d.mu.Unlock()

	select {
	case <-done:
		if d.cancel != nil {
			d.cancel()
		}
		d.finishResults()
		return nil
	case <-ctx.Done():
		select {
		case <-done:
			if d.cancel != nil {
				d.cancel()
			}
			d.finishResults()
			return nil
		default:
		}
	}

	d.cancelActive()
	grace := time.NewTimer(operationForcedShutdownGrace)
	defer grace.Stop()
	select {
	case <-done:
		d.finishResults()
		return fmt.Errorf("operation dispatcher required forced cancellation at shutdown deadline: %w", ctx.Err())
	case <-grace.C:
		d.finishResults()
		return fmt.Errorf("operation workers did not exit after forced cancellation: %w", ctx.Err())
	}
}

// Abort stops admission and cancels all accepted work immediately. It is used
// when the session loses authority (for example, lease revocation/expiry) or
// encounters a fatal transport/protocol failure. The caller supplies the
// total cleanup deadline; capabilities that ignore context cannot be killed
// by Go and are reported as a shutdown error.
func (d *OperationDispatcher) Abort(ctx context.Context) error {
	if ctx == nil {
		return errors.New("operation dispatcher abort context is required")
	}
	d.mu.Lock()
	if !d.started {
		d.mu.Unlock()
		return nil
	}
	d.stopAdmissionLocked()
	done := d.done
	d.mu.Unlock()

	d.cancelActive()
	select {
	case <-done:
		d.finishResults()
		return nil
	case <-ctx.Done():
		d.finishResults()
		return fmt.Errorf("operation dispatcher abort did not finish before cleanup deadline: %w", ctx.Err())
	}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
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
