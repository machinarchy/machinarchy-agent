# Agent Phase 4 — Bounded Operation Dispatch and Capability Execution

## Scope

Phase 4 replaces the Phase 3 validation-only event handler with a capability-oriented dispatcher. It accepts only explicitly registered capabilities. It does not implement arbitrary shell execution, SSH execution, scripts, file transfer, privilege escalation, or a general command interpreter.

The implementation includes:

- A capability registry sealed when the dispatcher is created.
- A bounded worker pool and bounded pending queue.
- Strict operation and cancellation payload decoding.
- Per-operation timeouts bounded by the event expiration.
- Context-based cancellation for queued and running operations.
- In-memory operation-ID duplicate suppression with bounded history.
- Accepted, rejected, succeeded, failed, timed-out, and cancelled results.
- Output validation and size limits.
- Panic isolation at the capability boundary.
- A result publisher using the existing agent NATS connection adapter.
- A minimal read-only `runtime.info` capability.
- Graceful queue draining and bounded forced cancellation at shutdown.

## Proposed event request contract

The Phase 3 envelope remains unchanged. For `event_type = operation.requested`, its `payload` is:

```json
{
  "operation_id": "operation-123",
  "capability": "runtime.info",
  "input": {},
  "timeout_ms": 30000
}
```

`timeout_ms` is optional. If omitted or zero, the configured maximum operation timeout is used. The effective timeout is the minimum of the request timeout, the configured maximum, and the remaining event lifetime.

For `event_type = operation.cancelled`, its `payload` is:

```json
{
  "operation_id": "operation-123",
  "reason": "cancelled by operator"
}
```

Unknown fields are rejected. `operation_id` and `capability` must be valid literal identifiers. `input` must be a JSON object and is subject to the configured input limit. Capability-specific input validation remains the responsibility of each registered capability.

## Capability model

A capability implements:

```go
type Capability interface {
    Execute(context.Context, json.RawMessage) (json.RawMessage, error)
}
```

Every capability must:

- Validate its own input schema.
- Honor context cancellation and deadlines.
- Avoid unbounded memory, CPU, network, and filesystem use.
- Return JSON output only.
- Avoid returning secrets or sensitive host information.

The initial built-in `runtime.info` capability returns agent version, Go version, operating system, architecture, and CPU count. It does not expose environment variables, files, credentials, network configuration, or execute a process.

## Proposed result contract

The agent publishes results to its agent-scoped subject:

```text
machinarchy.v1.agent.<agent_id>.operations.results
```

Result status progression is:

```text
accepted -> succeeded | failed | timed_out | cancelled
```

Rejected operations publish `rejected` with a stable error code such as:

- `capability_unavailable`
- `operation_queue_full`

Execution failures do not publish raw error strings, panic values, stack traces, or operation input. They use stable codes such as `capability_execution_failed`, `capability_panicked`, `operation_timeout`, `operation_cancelled`, and `operation_output_exceeded_limit`.

The result subject and result envelope are proposed contracts. The controller must implement the matching consumer, validate the publishing agent identity, enforce workspace/resource authorization, and reconcile the contract with `internal/eventing` and the accepted ADRs before production use.

## Configuration

| Variable | Default | Allowed range |
|---|---:|---:|
| `MACHINARCHY_AGENT_OPERATION_WORKERS` | `4` | 1–64 |
| `MACHINARCHY_AGENT_OPERATION_QUEUE_SIZE` | `64` | 1–4096 |
| `MACHINARCHY_AGENT_OPERATION_TIMEOUT` | `5m` | positive, at most 1h |
| `MACHINARCHY_AGENT_OPERATION_MAX_INPUT_BYTES` | `65536` | 1–65536 |
| `MACHINARCHY_AGENT_OPERATION_MAX_OUTPUT_BYTES` | `262144` | 1–262144 |
| `MACHINARCHY_AGENT_RESULT_PUBLISH_TIMEOUT` | `5s` | positive duration |

The operation timeout is an upper bound, not a promise that a capability will finish within that time. Capabilities must cooperate with context cancellation. Go cannot safely kill a goroutine that ignores its context.

## Delivery and durability limitations

This phase is not a durable execution engine.

- The current transport is Core NATS. It does not provide durable replay or explicit consumer acknowledgements.
- The bounded worker queue is in memory. An agent process crash can lose work that was reported as accepted.
- The existing event ledger records recent event IDs, but it is not an operation journal.
- Operation-ID duplicate suppression is in-memory and bounded to the most recent 4,096 records. It is not a durable idempotency guarantee across process restarts.
- A successful NATS flush confirms that the broker received a result publication; it does not prove that the controller processed or persisted the result.
- Final result publication is retried a small bounded number of times. There is no durable result outbox in this agent phase.

Do not claim exactly-once execution, at-least-once delivery, or durable operation completion. Before production remote operations, design and implement a durable operation journal/recovery policy, a durable broker delivery strategy or database-backed work polling, and a transactional result outbox/acknowledgement protocol with the controller.

## Security and broker permissions

The broker ACL for an enrolled agent should allow only:

- Request/reply to the approved registration and lease-renewal subjects (as long as enrollment policy permits it).
- Subscribe to `machinarchy.v1.agent.<assigned_agent_id>.events` only.
- Publish to `machinarchy.v1.agent.<assigned_agent_id>.operations.results` only.

The agent must not be able to subscribe to wildcard fleet subjects or publish results for another agent. The controller must not trust `agent_id` in a result without binding it to the authenticated NATS identity/credentials.

## Verification

The source bundle includes unit tests for:

- Capability registration and validation.
- Successful capability execution and output publication.
- Unknown capability rejection.
- Malformed payload rejection.
- Queue saturation and rejection.
- Running operation cancellation.
- Per-operation timeout.
- Duplicate operation-ID suppression.
- Cancellation arriving before the operation request.
- Output-size enforcement.
- Agent-scoped result subject construction.
- Shutdown and admission stop.

The bundle's standalone verification module uses a compile-time NATS API stub. Run the same checks against the real repository `go.mod` and the pinned NATS dependency after integration. Add real broker integration and controller-to-agent end-to-end tests before enabling operation execution in production.
