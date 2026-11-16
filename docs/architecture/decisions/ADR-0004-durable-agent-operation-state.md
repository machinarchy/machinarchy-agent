# ADR-0004: Durable Agent Operation State and Result Outbox

- Status: Accepted
- Date: 2026-11-14

## Context

The Agent executes operations that may have external side effects. An in-memory
queue and result publisher cannot distinguish safely between work that never
started and work that may have completed before a process crash. Automatically
replaying a command after an ambiguous crash could duplicate a deployment,
restart, migration or other side effect. Results must also survive temporary
controller or broker unavailability.

## Decision

The Agent uses a bounded local `OperationStore` abstraction. The initial
implementation is a private, atomically replaced JSON snapshot in
the Agent state directory; it introduces no external persistence dependency.
The file is mode `0600`, its parent directory must be mode `0700`, and only one
Agent process may use a state directory. The store has explicit record, output
and pending-operation bounds.

The Agent persists acceptance before queueing and persists `running` before
calling a capability. Once `running` is durable, raw request/event payloads are
removed from the snapshot and a digest remains for duplicate-ID detection. On
restart it may resume unexpired operations still in `accepted`; it converts
`running` or `cancel_requested` operations to the terminal `unknown` outcome
and does not replay them. Terminal results remain in a local outbox until the
controller acknowledges durable persistence using the protocol v2
`operation.result.acknowledged` event. The Agent retries pending results and
applies backpressure when its bounded outstanding-operation capacity is
exhausted. After acknowledgement, result payloads are cleared while compact
deduplication metadata remains until the original event expires.

## Consequences

- Controller result handling must be idempotent by `operation_id` and must
  acknowledge only after its durable transaction commits.
- The controller must understand the `unknown` result state. Agent protocol v2
  and controller support must be deployed as a coordinated compatibility
  change.
- The Agent-side store is complemented by JetStream durable inbound delivery
  as decided in ADR-0005. Controller stream/consumer provisioning and outbox
  integration remain external deployment requirements until integrated and
  tested in the controller repository.
- The snapshot store is intentionally bounded and single-process. If observed
  write amplification or throughput requires it, a SQLite implementation can
  replace the adapter without changing dispatcher semantics. That change
  requires its own measurement and ADR/update.
- Exactly-once execution of arbitrary external side effects is not promised.
