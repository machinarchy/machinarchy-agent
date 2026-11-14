# Agent Phase 3 — Event Subscription and Validation

## Status

**Agent-side subscription and validation implementation drafted. The event envelope and subject are proposed contracts, not yet shared controller contracts.**

The current agent bundle cannot establish compatibility with the repository's actual `internal/eventing/contracts.go`, accepted ADR-016/017/018/031/050, or existing `internal/broker` package because those source files were not part of the source bundle available for this implementation. Reconcile the contract before controller publication or production use.

## Scope

Phase 3 adds:

1. An agent-specific Core NATS subscription created only after registration and successful lease renewal.
2. A versioned JSON event envelope.
3. Strict envelope decoding with unknown-field rejection.
4. Message-size and payload-shape bounds.
5. Agent and host target checks.
6. Event type, correlation ID, timestamp, expiration, and protocol-version validation.
7. A bounded local NATS channel subscription.
8. A private on-disk deduplication ledger retaining the most recent 4,096 event IDs.
9. Graceful subscription cancellation and shutdown.

This phase does **not** implement operation dispatch, execution, cancellation of running operations, or durable broker delivery.

## Proposed subject

The agent subscribes to exactly one subject after obtaining its control-plane-assigned identity and establishing a valid lease. Lease expiry is checked before every event is handled; the processor identity is refreshed only after a successful lease renewal.

```text
machinarchy.v1.agent.<agent_id>.events
```

The agent ID must be a single NATS subject token containing only ASCII letters, digits, `_`, or `-`. Dots, wildcards, spaces, and other punctuation are rejected. A typical UUID is valid.

NATS permissions should allow an agent to subscribe only to its own event subject. The controller must publish only events authorized for that exact agent and host. Do not grant wildcard subscriptions such as `machinarchy.v1.agent.>` to ordinary agents.

## Proposed version-1 event envelope

```json
{
  "protocol_version": 1,
  "event_id": "event-uuid-or-token",
  "event_type": "operation.requested",
  "target_agent_id": "agent-id-assigned-by-controller",
  "target_host_id": "host-id-assigned-by-controller",
  "correlation_id": "request-correlation-id",
  "issued_at": "2026-11-14T00:00:00Z",
  "expires_at": "2026-11-14T00:01:00Z",
  "payload": {
    "operation_id": "operation-id",
    "...": "event-type-specific fields are defined with the dispatcher contract"
  }
}
```

Supported event types in this phase:

- `operation.requested`
- `operation.cancelled`

The payload is required to be a JSON object and is size-bounded, but Phase 3 deliberately does not define its operation-specific fields. Those belong to the shared eventing and operation contracts used by Phase 4.

### Validation rules

- `protocol_version` must equal `1`.
- Unknown top-level envelope fields are rejected.
- Exactly one JSON value is allowed.
- The full event must not exceed 256 KiB; the payload must not exceed 128 KiB.
- `event_id` and `correlation_id` must be non-empty NATS-safe identifier tokens, at most 128 characters.
- `target_agent_id` and `target_host_id` must exactly match the registered local identity.
- Only the two event types above are accepted.
- `issued_at` and `expires_at` are required.
- `issued_at` must not be more than two minutes in the future.
- `expires_at` must be in the future and after `issued_at`.
- The interval between issue and expiration must not exceed 24 hours.

The current registration identity does not contain a workspace ID, so the agent cannot independently compare an event's workspace against a local workspace claim. Workspace authorization must be enforced by the controller before publication and by NATS account/subject permissions. If independent workspace validation is required, add the authoritative workspace binding to the accepted identity/event contracts in a separate ADR-compatible change; do not infer it from host labels.

## Bounded subscription and delivery semantics

`MACHINARCHY_AGENT_EVENT_BUFFER_SIZE` defaults to `64` and accepts values from `1` through `256`.

The agent uses an asynchronous Core NATS subscription with bounded message-count and pending-byte limits, plus a bounded local channel. Oversized events are rejected before enqueue. A slow consumer can trigger a NATS slow-consumer error and messages may be dropped. This is not a durable queue.

Core NATS does not provide persistent replay or explicit message acknowledgements. Reconnection restores the subscription, but messages published while the agent is disconnected are not replayed by Core NATS. The event ledger suppresses repeated event IDs while they remain in its recent 4,096-ID window; it does not create delivery guarantees.

Do not claim at-least-once or exactly-once delivery. Before relying on reliable operation delivery, choose and implement the delivery model in accordance with the repository's accepted ADRs (for example, a transactional outbox plus an explicitly selected durable consumer mechanism, if later approved). Do not add JetStream merely as a side effect of Phase 3.

## Deduplication ledger

The ledger path is derived from the configured identity path:

```text
<identity-file>.events.json
```

The ledger stores only event IDs and a schema version. It does not store payloads or secret material. It is atomically rewritten, uses file mode `0600`, requires a private parent directory (`0700`), rejects symlinks, and retains at most 4,096 recent IDs.

The recent-ID window is bounded. An event older than the retained window can be accepted again if it is replayed. Phase 4 must add operation-ID idempotency against durable operation state; the event-ID ledger alone is not sufficient protection for side-effecting execution.

## Current handler behavior — important

The default Phase 3 handler validates and records accepted events but **does not execute them**. It logs a warning containing only event ID, event type, and correlation ID. The payload is never logged.

The controller must not start publishing real operation commands to this subscription until Phase 4 replaces the validation-only handler with an approved bounded dispatcher and the end-to-end delivery contract is implemented. In the present phase, a received valid event is consumed by the Core NATS subscription and will not be automatically replayed.

## Agent configuration

```bash
export MACHINARCHY_AGENT_EVENT_BUFFER_SIZE=64
```

The existing Phase 1 and Phase 2 broker, enrollment, identity, and lease settings remain required. The event subject is derived from the registered identity and is not freely configurable by the agent.

## Controller and shared-contract work required

Before production publication:

1. Reconcile this envelope with `internal/eventing/contracts.go` and ADR-016/017/018/031/050.
2. Move the approved envelope and event-type constants into the shared eventing package.
3. Implement controller-side routing to the exact agent subject.
4. Enforce workspace, host, and agent authorization before publication.
5. Configure NATS ACLs for per-agent subscriptions and control-plane request/reply permissions.
6. Define the operation payload schema jointly with the Phase 4 dispatcher.
7. Decide how events are recovered after agent disconnection and how publisher retries interact with event and operation idempotency.
8. Add real NATS integration tests, including reconnect and slow-consumer behavior.

## Verification performed for this bundle

The complete agent source in this bundle passed:

```bash
gofmt -w cmd/agent internal/agent
go test ./...
go test -race ./internal/agent ./cmd/agent
go vet ./...
```

These checks were run in an isolated Go module with a compile-time NATS API stub because the actual repository `go.mod` and existing broker package were not provided as source. Unit tests exercise event validation, identity/host targeting, time bounds, persistent deduplication, session subscription lifecycle, and shutdown. This is not a live NATS test and is not a build against the exact NATS dependency version pinned by the Machinarchy repository.
