# Machinarchy Agent — Phase 4 Summary

Phase 4 introduces bounded, capability-oriented operation execution.

## Implemented

- `OperationDispatcher` implements the agent event-handler boundary.
- Capability registry with registration validation and sealing.
- Built-in read-only `runtime.info` capability.
- Bounded worker pool and pending queue.
- Operation request/cancellation parsing and strict validation.
- Deadline derived from request timeout, configured maximum, and event expiry.
- Operation cancellation for queued/running work and pre-cancellation tombstones.
- In-memory bounded operation ID duplicate suppression.
- Accepted/rejected/final result publication to an agent-scoped NATS subject.
- Output size and JSON validation.
- Capability panic isolation without logging panic values or stack traces.
- Dispatcher shutdown closes admission, drains queue, then forces cancellation after timeout.
- Agent session wires dispatcher after registration/lease and closes subscription before draining workers.
- Configuration and tests for all new bounds.

## Explicit non-goals

- Arbitrary shell or process execution.
- SSH command execution.
- Filesystem mutation or file transfer.
- Agent-local HTTP/gRPC listener.
- Durable operation journal or crash recovery.
- Durable broker delivery or exactly-once execution.
- Durable result outbox/controller acknowledgement.

## Proposed wire contract

The request and result subjects in `docs/agent/phase-4-operation-dispatcher.md` need controller implementation and review against the accepted ADRs and shared `internal/eventing` contracts. They must not be treated as an already-existing controller contract.

## Verification

The bundle is verified with its isolated `phase4-verification.mod` and compile-time NATS stub. This proves local compilation and unit-test behavior against the stubbed API surface only. Re-run all checks with the real Machinarchy `go.mod`, pinned NATS version, and live NATS/controller integration before merging or deploying.
