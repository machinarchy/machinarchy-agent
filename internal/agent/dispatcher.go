package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/machinarchy/machinarchy-protocol/agent/v1"
)

const (
	operationProtocolVersion     = agentv1.ProtocolVersion
	defaultOperationWorkers      = 4
	defaultOperationQueue        = 64
	maxOperationWorkers          = 64
	maxOperationQueue            = 4096
	maxOperationInputBytes       = 64 * 1024
	maxOperationOutputBytes      = 256 * 1024
	maxOperationTimeout          = time.Hour
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
	if len(payload) > maxOperationOutputBytes+16*1024 {
		return errors.New("operation result exceeds the maximum message size")
	}
	return p.publisher.Publish(ctx, subject, payload)
}

func AgentOperationResultsSubject(agentID string) (string, error) {
	return agentv1.AgentOperationResultsSubject(agentID)
}

// Capability executes one explicitly registered operation. Implementations
// must honor context cancellation and validate the capability-specific input.
type Capability interface {
	Execute(context.Context, json.RawMessage) (json.RawMessage, error)
}

type CapabilityFunc func(context.Context, json.RawMessage) (json.RawMessage, error)

func (f CapabilityFunc) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	return f(ctx, input)
}

type CapabilityRegistry struct {
	mu           sync.RWMutex
	capabilities map[string]Capability
	sealed       bool
}

func NewCapabilityRegistry() *CapabilityRegistry {
	return &CapabilityRegistry{capabilities: make(map[string]Capability)}
}

func (r *CapabilityRegistry) Register(name string, capability Capability) error {
	if r == nil {
		return errors.New("capability registry is required")
	}
	if !validCapabilityName(name) {
		return errors.New("capability name has an invalid format")
	}
	if capability == nil {
		return errors.New("capability implementation is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return errors.New("capability registry is sealed")
	}
	if _, exists := r.capabilities[name]; exists {
		return fmt.Errorf("capability %q is already registered", name)
	}
	r.capabilities[name] = capability
	return nil
}

func (r *CapabilityRegistry) lookup(name string) (Capability, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	capability, ok := r.capabilities[name]
	return capability, ok
}

func (r *CapabilityRegistry) seal() {
	r.mu.Lock()
	r.sealed = true
	r.mu.Unlock()
}

func validCapabilityName(name string) bool {
	if name == "" || len(name) > 128 || strings.TrimSpace(name) != name {
		return false
	}
	for _, part := range strings.Split(name, ".") {
		if part == "" {
			return false
		}
		for _, char := range part {
			if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '_' && char != '-' {
				return false
			}
		}
	}
	return true
}

// RuntimeInfoCapability exposes a deliberately small, read-only subset of
// process metadata. It does not expose environment variables, filesystem
// contents, credentials, network configuration, or arbitrary command output.
func RuntimeInfoCapability(agentVersion string) Capability {
	return CapabilityFunc(func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := requireEmptyObject(input); err != nil {
			return nil, err
		}
		result := struct {
			AgentVersion string `json:"agent_version"`
			GoVersion    string `json:"go_version"`
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
			CPUCount     int    `json:"cpu_count"`
		}{AgentVersion: agentVersion, GoVersion: runtime.Version(), OS: runtime.GOOS, Architecture: runtime.GOARCH, CPUCount: runtime.NumCPU()}
		return json.Marshal(result)
	})
}

func requireEmptyObject(raw json.RawMessage) error {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return errors.New("capability input must be a JSON object")
	}
	if len(value) != 0 {
		return errors.New("capability does not accept input fields")
	}
	return nil
}

type OperationDispatcherConfig struct {
	Workers              int
	QueueSize            int
	MaxOperationTimeout  time.Duration
	MaxInputBytes        int
	MaxOutputBytes       int
	ResultPublishTimeout time.Duration
}

func (c Config) OperationDispatcherConfig() OperationDispatcherConfig {
	return OperationDispatcherConfig{
		Workers: c.OperationWorkers, QueueSize: c.OperationQueueSize,
		MaxOperationTimeout: c.MaxOperationTimeout, MaxInputBytes: c.MaxOperationInputBytes,
		MaxOutputBytes: c.MaxOperationOutputBytes, ResultPublishTimeout: c.ResultPublishTimeout,
	}
}

type operationRecord struct {
	status OperationStatus
	cancel context.CancelFunc
}

type queuedOperation struct {
	request    OperationRequestPayload
	event      AgentEventEnvelope
	capability Capability
	ctx        context.Context
	cancel     context.CancelFunc
	acceptedAt time.Time
}

// OperationDispatcher is both the Phase 3 event handler and a bounded worker
// pool. It never interprets an operation as a shell command.
type OperationDispatcher struct {
	identity  AgentIdentity
	registry  *CapabilityRegistry
	publisher OperationResultPublisher
	config    OperationDispatcherConfig
	logger    *slog.Logger

	mu           sync.Mutex
	started      bool
	accepting    bool
	stopping     bool
	queue        chan *queuedOperation
	slots        chan struct{}
	records      map[string]operationRecord
	order        []string
	ctx          context.Context
	cancel       context.CancelFunc
	resultCtx    context.Context
	resultCancel context.CancelFunc
	workers      sync.WaitGroup
	done         chan struct{}
}

func NewOperationDispatcher(identity AgentIdentity, registry *CapabilityRegistry, publisher OperationResultPublisher, config OperationDispatcherConfig, logger *slog.Logger) (*OperationDispatcher, error) {
	if err := identity.Validate(); err != nil || !identity.Registered() {
		return nil, errors.New("registered agent identity is required for operation dispatch")
	}
	if registry == nil {
		return nil, errors.New("capability registry is required")
	}
	if publisher == nil {
		return nil, errors.New("operation result publisher is required")
	}
	if config.Workers < 1 || config.Workers > maxOperationWorkers || config.QueueSize < 1 || config.QueueSize > maxOperationQueue {
		return nil, errors.New("operation workers or queue size are outside supported limits")
	}
	if config.MaxOperationTimeout <= 0 || config.MaxOperationTimeout > maxOperationTimeout || config.MaxInputBytes < 1 || config.MaxInputBytes > maxOperationInputBytes || config.MaxOutputBytes < 1 || config.MaxOutputBytes > maxOperationOutputBytes || config.ResultPublishTimeout <= 0 {
		return nil, errors.New("operation timeout, payload limits, or result publish timeout are invalid")
	}
	if logger == nil {
		logger = slog.Default()
	}
	registry.seal()
	return &OperationDispatcher{
		identity: identity, registry: registry, publisher: publisher, config: config,
		logger: logger.With("component", "agent-operation-dispatcher"),
		queue:  make(chan *queuedOperation, config.QueueSize), slots: make(chan struct{}, config.QueueSize),
		records: make(map[string]operationRecord), done: make(chan struct{}),
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
	defer d.mu.Unlock()
	if d.started {
		return errors.New("operation dispatcher has already started")
	}
	// Operation contexts must not be cancelled immediately when the session
	// context ends. Session shutdown first closes inbound delivery, then gives
	// accepted work the configured drain window. Fatal session failures use
	// Abort to cancel work immediately. WithoutCancel preserves context values
	// while detaching worker lifetime from the caller's cancellation signal.
	d.ctx, d.cancel = context.WithCancel(context.WithoutCancel(parent))
	d.resultCtx, d.resultCancel = context.WithCancel(context.Background())
	d.started = true
	d.accepting = true
	for range d.config.Workers {
		d.workers.Add(1)
		go d.worker()
	}
	go func() {
		d.workers.Wait()
		close(d.done)
	}()
	return nil
}

func (d *OperationDispatcher) Handle(ctx context.Context, event AgentEventEnvelope) error {
	if ctx == nil {
		return errors.New("operation event context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch event.EventType {
	case AgentEventOperationRequested:
		return d.handleRequest(ctx, event)
	case AgentEventOperationCancelled:
		return d.handleCancellation(ctx, event)
	default:
		return &InvalidAgentEventError{reason: "unsupported operation event type"}
	}
}

func (d *OperationDispatcher) handleRequest(ctx context.Context, event AgentEventEnvelope) error {
	request, err := decodeOperationRequest(event.Payload, d.config.MaxInputBytes)
	if err != nil {
		return err
	}
	capability, ok := d.registry.lookup(request.Capability)
	if !ok {
		return d.publishSimpleResult(ctx, event, request.OperationID, request.Capability, OperationStatusRejected, "capability_unavailable")
	}
	if request.TimeoutMilli == 0 {
		request.TimeoutMilli = d.config.MaxOperationTimeout.Milliseconds()
	}
	timeout := time.Duration(request.TimeoutMilli) * time.Millisecond
	if timeout <= 0 {
		return &InvalidAgentEventError{reason: "operation timeout must be positive"}
	}
	if timeout > d.config.MaxOperationTimeout {
		timeout = d.config.MaxOperationTimeout
	}
	remaining := time.Until(event.ExpiresAt)
	if remaining <= 0 {
		return &InvalidAgentEventError{reason: "operation event expired before dispatch"}
	}
	if timeout > remaining {
		timeout = remaining
	}

	d.mu.Lock()
	if !d.started || !d.accepting {
		d.mu.Unlock()
		return errors.New("operation dispatcher is not accepting work")
	}
	if existing, exists := d.records[request.OperationID]; exists {
		d.mu.Unlock()
		if existing.status == OperationStatusCancelled {
			return d.publishSimpleResult(ctx, event, request.OperationID, request.Capability, OperationStatusCancelled, "cancelled_before_execution")
		}
		d.logger.Debug("duplicate operation request ignored", "operation_id", request.OperationID)
		return nil
	}
	select {
	case d.slots <- struct{}{}:
	default:
		d.mu.Unlock()
		return d.publishSimpleResult(ctx, event, request.OperationID, request.Capability, OperationStatusRejected, "operation_queue_full")
	}
	opCtx, cancel := context.WithTimeout(d.ctx, timeout)
	op := &queuedOperation{request: request, event: event, capability: capability, ctx: opCtx, cancel: cancel, acceptedAt: time.Now().UTC()}
	d.records[request.OperationID] = operationRecord{status: OperationStatusAccepted, cancel: cancel}
	d.rememberLocked(request.OperationID)
	d.mu.Unlock()

	accepted := d.result(event, request.OperationID, request.Capability, OperationStatusAccepted, "", nil)
	accepted.AcceptedAt = op.acceptedAt
	if err := d.publishResult(ctx, accepted); err != nil {
		cancel()
		d.mu.Lock()
		delete(d.records, request.OperationID)
		<-d.slots
		d.mu.Unlock()
		return fmt.Errorf("publish operation acceptance: %w", err)
	}

	d.mu.Lock()
	if !d.accepting {
		delete(d.records, request.OperationID)
		<-d.slots
		d.mu.Unlock()
		cancel()
		return errors.New("operation dispatcher stopped before operation enqueue")
	}
	d.queue <- op // A slot was reserved before publishing acceptance, so this send is bounded and cannot block.
	d.mu.Unlock()
	return nil
}

func (d *OperationDispatcher) handleCancellation(ctx context.Context, event AgentEventEnvelope) error {
	var request OperationCancellationPayload
	if err := decodeStrictObject(event.Payload, &request, 1024); err != nil {
		return &InvalidAgentEventError{reason: "operation cancellation payload is invalid"}
	}
	if !validEventIdentifier(request.OperationID) || len(request.Reason) > 256 || strings.ContainsAny(request.Reason, "\r\n\x00") {
		return &InvalidAgentEventError{reason: "operation cancellation fields are invalid"}
	}
	d.mu.Lock()
	record, exists := d.records[request.OperationID]
	if !exists {
		d.records[request.OperationID] = operationRecord{status: OperationStatusCancelled}
		d.rememberLocked(request.OperationID)
		d.mu.Unlock()
		return d.publishSimpleResult(ctx, event, request.OperationID, "", OperationStatusCancelled, "cancelled_before_execution")
	}
	if record.status == OperationStatusAccepted || record.status == OperationStatusRunning || record.status == OperationStatusCancelRequest {
		if record.cancel != nil {
			record.cancel()
		}
		// Keep the record non-terminal until the worker has actually exited.
		// Otherwise a high admission rate could prune it while execution is still unwinding.
		d.records[request.OperationID] = operationRecord{status: OperationStatusCancelRequest, cancel: record.cancel}
	}
	d.mu.Unlock()
	d.logger.Info("operation cancellation requested", "operation_id", request.OperationID)
	return nil
}

func decodeOperationRequest(payload json.RawMessage, maxInput int) (OperationRequestPayload, error) {
	var request OperationRequestPayload
	if err := decodeStrictObject(payload, &request, maxOperationInputBytes+4096); err != nil {
		return OperationRequestPayload{}, &InvalidAgentEventError{reason: "operation request payload is invalid"}
	}
	if !validEventIdentifier(request.OperationID) || !validCapabilityName(request.Capability) {
		return OperationRequestPayload{}, &InvalidAgentEventError{reason: "operation identifier or capability name is invalid"}
	}
	if len(request.Input) == 0 || len(request.Input) > maxInput {
		return OperationRequestPayload{}, &InvalidAgentEventError{reason: "operation input is empty or exceeds the configured limit"}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(request.Input, &object); err != nil || object == nil {
		return OperationRequestPayload{}, &InvalidAgentEventError{reason: "operation input must be a JSON object"}
	}
	if request.TimeoutMilli < 0 || request.TimeoutMilli > maxOperationTimeout.Milliseconds() {
		return OperationRequestPayload{}, &InvalidAgentEventError{reason: "operation timeout is outside the supported range"}
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
	d.records[op.request.OperationID] = operationRecord{status: OperationStatusRunning, cancel: op.cancel}
	d.mu.Unlock()

	output, err := safeExecuteCapability(op.ctx, op.capability, op.request.Input)
	status := OperationStatusSucceeded
	code := ""
	if err != nil {
		switch {
		case errors.Is(op.ctx.Err(), context.DeadlineExceeded):
			status, code = OperationStatusTimedOut, "operation_timeout"
		case errors.Is(op.ctx.Err(), context.Canceled):
			status, code = OperationStatusCancelled, "operation_cancelled"
		default:
			if errors.Is(err, errCapabilityPanicked) {
				status, code = OperationStatusFailed, "capability_panicked"
			} else {
				status, code = OperationStatusFailed, "capability_execution_failed"
			}
		}
		output = nil
	} else if len(output) == 0 || !json.Valid(output) {
		status, code, output = OperationStatusFailed, "capability_returned_invalid_json", nil
	} else if len(output) > d.config.MaxOutputBytes {
		status, code, output = OperationStatusFailed, "operation_output_exceeded_limit", nil
	}
	d.complete(op, status, code, output, started)
}

var errCapabilityPanicked = errors.New("capability panicked")

func safeExecuteCapability(ctx context.Context, capability Capability, input json.RawMessage) (output json.RawMessage, err error) {
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
	d.mu.Lock()
	d.records[op.request.OperationID] = operationRecord{status: status}
	d.mu.Unlock()

	result := d.result(op.event, op.request.OperationID, op.request.Capability, status, code, output)
	result.AcceptedAt = op.acceptedAt
	result.StartedAt = started
	result.CompletedAt = completed
	result.DurationMillis = completed.Sub(started).Milliseconds()
	if err := d.publishResultDetached(result); err != nil {
		d.logger.Error("publish operation result failed", "operation_id", op.request.OperationID, "status", status, "error_code", "result_publish_failed")
		return
	}
	d.logger.Info("operation completed", "operation_id", op.request.OperationID, "capability", op.request.Capability, "status", status, "duration_ms", result.DurationMillis)
}

func (d *OperationDispatcher) publishSimpleResult(ctx context.Context, event AgentEventEnvelope, operationID, capability string, status OperationStatus, code string) error {
	result := d.result(event, operationID, capability, status, code, nil)
	result.CompletedAt = time.Now().UTC()
	if err := d.publishResult(ctx, result); err != nil {
		return fmt.Errorf("publish operation %s result: %w", status, err)
	}
	return nil
}

func (d *OperationDispatcher) result(event AgentEventEnvelope, operationID, capability string, status OperationStatus, code string, output json.RawMessage) OperationResult {
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
	case OperationStatusSucceeded, OperationStatusFailed, OperationStatusCancelled, OperationStatusTimedOut, OperationStatusRejected:
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

func defaultCapabilityRegistry(agentVersion string) (*CapabilityRegistry, error) {
	registry := NewCapabilityRegistry()
	if err := registry.Register("runtime.info", RuntimeInfoCapability(agentVersion)); err != nil {
		return nil, err
	}
	return registry, nil
}
