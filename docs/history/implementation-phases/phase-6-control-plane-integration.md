# Agent Phase 6 — Shared Protocol and Control-Plane Integration

## Status

**Shared contract package and transport-neutral controller handlers implemented. Actual application wiring remains an integration task.**

This phase addresses the mismatch between an agent-only protocol implementation and a control plane that has not yet registered the corresponding handlers. It intentionally does not invent the signatures of the repository's existing `internal/broker`, `internal/store/resources/outbox.go`, service, or resource repository APIs.

## What this phase adds

### 1. One shared wire contract

`internal/eventing/agentv1` now owns the v1 JSON wire types used by both sides:

- Registration request and response.
- Lease renewal request and response.
- Versioned agent event envelope.
- Operation request and cancellation payloads.
- Operation result payload and status values.
- Canonical registration, lease, event, and result subjects.

The agent uses type aliases to these shared definitions. Agent-specific validation involving its persisted identity remains in `internal/agent`; protocol definitions do not depend on agent runtime or controller domain models.

This is a new subpackage under the existing `internal/eventing` boundary. Before merging, compare it with the repository's current `internal/eventing/contracts.go`; move/merge equivalent contracts rather than preserving duplicate v1 schemas.

### 2. Transport-neutral controller request handlers

`internal/agentcontrol.Handler` implements:

- `HandleRegistration` for `machinarchy.v1.agent.register`.
- `HandleLeaseRenewal` for `machinarchy.v1.agent.lease.renew`.
- `HandleOperationResultMessage` for agent-scoped operation result messages.
- `Register` against a narrow `HandlerRegistrar` adapter contract.

The handlers strictly decode JSON, reject unsupported protocol versions and malformed identities, validate lease windows and result status, and avoid exposing internal service errors to agents.

`LifecycleService` and `OperationResultStore` are application boundaries. They must be implemented using Machinarchy's existing domain/service/store packages.

### 3. Transactional event publication boundary

`internal/agentcontrol.EventPublisher` builds target-scoped, versioned operation/cancellation envelopes and writes them to an `EventOutbox`.

It does not publish directly to NATS. The real outbox implementation must persist the operation state change and its event in the same database transaction. A separate outbox dispatcher publishes pending rows through the existing broker abstraction.

## Required application wiring

The source tree provided for review contains the relevant packages, but not their source signatures. The following adapters must be implemented against the actual repository APIs rather than guessed here.

### A. Lifecycle service adapter

Implement `agentcontrol.LifecycleService` in the existing service layer.

`RegisterAgent` must:

1. Validate and consume a one-time enrollment token.
2. Enforce workspace/host enrollment policy and authorization.
3. Treat `request_id` as an idempotency key.
4. In one transaction, consume the enrollment token, create or retrieve the assigned agent identity, create its initial lease, and record the request ID.
5. Return the same agent/host/lease result when the same valid request ID is retried.
6. Never store or log the raw enrollment token.

`RenewAgentLease` must:

1. Resolve the agent and host from controller-owned records.
2. Verify the lease ID and credential using a constant-time comparison or a one-way credential verifier.
3. Reject revoked, expired, stale, or mismatched leases.
4. Rotate lease credentials where required by the accepted ADR.
5. Persist the renewal atomically and return the newly acknowledged expiry/heartbeat interval.

Do not trust `hostname`, `host_id`, or `agent_id` supplied by the agent as authority. Only the enrollment record and persisted identity determine the association.

### B. Persistence and migrations

Review the existing migrations before adding schema. The current tree listing does not show a confirmed complete agent identity/lease persistence model.

At minimum, the persistence design needs durable representations for:

- Enrollment token digest, scope, expiry, revocation, and one-time consumption.
- Agent identity and its binding to the authoritative host/workspace.
- Lease identifier, credential digest, expiry, rotation/version and revocation state.
- Registration request idempotency key and resulting identity.
- Operation result history/state transitions with agent/host/operation ownership checks.
- Event outbox rows for controller-to-agent commands.

Implement equivalent migrations for SQLite and PostgreSQL. Follow the existing migration and transaction conventions. Do not keep this state in process memory.

### C. Broker adapter

Implement `agentcontrol.HandlerRegistrar` beside the existing `internal/broker` implementation, using the real API exposed by that package. Register handlers during the control-manager startup lifecycle.

The adapter must:

- Support request/reply handlers for registration and lease renewal.
- Subscribe to operation results with bounded pending messages/bytes.
- Propagate cancellation and close subscriptions on shutdown.
- Avoid logging request bodies or credentials.
- Surface broker disconnect/slow-consumer errors to readiness/telemetry.

Do not create a second NATS connection abstraction if `internal/broker` already owns connection lifecycle. Reconcile `internal/agent/nats.go` with the existing broker package as a separate code review against its actual exported API.

### D. Outbox adapter and publisher

Adapt the `agentcontrol.EventOutbox` interface to the existing resource outbox implementation. Use the same transaction as the operation state change. The outbox dispatcher should publish the row and mark it delivered only according to the delivery semantics provided by the chosen broker.

Core NATS publish/flush is not a durable consumer acknowledgement. Do not mark a command as durably delivered merely because a publish call returned nil or a flush succeeded.

### E. Result sink adapter

Implement `OperationResultStore.RecordAgentOperationResult` against the existing operation/resource service and store contracts.

The store must:

- Verify agent identity, host binding, workspace scope, and operation target.
- Reject unknown operations and invalid status transitions.
- Make duplicate result delivery idempotent.
- Prevent late results from overwriting a newer terminal state.
- Persist result metadata and bounded output according to retention policy.
- Redact or omit sensitive output before audit/telemetry.

### F. End-to-end tests

Add tests using a real NATS server/container or the repository's established broker integration harness.

Required scenarios:

1. Valid enrollment creates one agent and one lease.
2. Retrying the same registration request ID returns the same identity.
3. A consumed/revoked/expired token cannot enroll another agent.
4. A lease renewal with the wrong credential is rejected.
5. A revoked/stale lease cannot publish work or renew.
6. Controller publishes an operation event from the transactional outbox.
7. Only the intended agent receives and accepts the event.
8. Agent publishes a result and controller persists it.
9. Duplicate results do not create duplicate state transitions.
10. Workspace/host/agent spoofing is rejected.
11. Broker disconnect and reconnect behavior is exercised.
12. Oversized, malformed, expired and unsupported-version messages are rejected.

## NATS subject and ACL model

Use per-agent credentials and deny-by-default subject permissions.

| Principal | Publish | Subscribe |
|---|---|---|
| Controller | `machinarchy.v1.agent.*.events` | `machinarchy.v1.agent.register`, `machinarchy.v1.agent.lease.renew`, `machinarchy.v1.agent.*.operations.results` |
| Agent | `machinarchy.v1.agent.register`, `machinarchy.v1.agent.lease.renew`, its own `machinarchy.v1.agent.<agent_id>.operations.results` | its own `machinarchy.v1.agent.<agent_id>.events`, NATS request/reply inbox subjects required by the client |

The agent must not be allowed to publish another agent's result subject or subscribe to another agent's event subject. The controller must verify the subject agent ID matches the `agent_id` in each result payload and verify the host/operation association in persistence.

NATS inbox permissions must be scoped to the inbox pattern used by the client/runtime; do not grant broad access to arbitrary application subjects as a shortcut.

## Delivery semantics and safety gate

The current agent implementation uses Core NATS request/reply and subscriptions. Core NATS does not replay messages missed while an agent is offline and does not provide explicit consumer acknowledgements. A transactional controller outbox protects the database-to-publisher boundary, but it does not alone guarantee durable agent delivery or exactly-once execution.

Before enabling non-idempotent or privileged capabilities, choose and document one of:

- JetStream durable consumers with explicit acknowledgement, redelivery limits, and a deduplication strategy; or
- A controller-authoritative pull/poll protocol backed by durable operation records and leases.

Even with durable transport, design operations for idempotency and recoverable state transitions. Do not claim exactly-once execution.

For the current implementation, keep the only built-in capability read-only `runtime.info` and treat the agent as an integration preview until the controller wiring and end-to-end tests are complete.

## Verification

The Phase 6 bundle passes isolated tests, race tests, vet, and agent build against the included compile-time NATS API stub. This proves the local package contracts and tests compile; it does not prove compatibility with the repository's real `internal/broker`, `internal/store`, actual pinned NATS dependency, database schema, or live controller.

After copying the files into the actual repository, run:

```bash
gofmt -w cmd/agent internal/agent internal/eventing/agentv1 internal/agentcontrol
go test ./internal/agent ./internal/agentcontrol ./internal/eventing/agentv1 ./cmd/agent
go test -race ./internal/agent ./internal/agentcontrol ./internal/eventing/agentv1 ./cmd/agent
go test ./...
go vet ./...
go build ./cmd/agent ./cmd/controlmanager ./cmd/api ./cmd/monolit
```

Do not mark Phase 6 production complete until the actual broker/store/service adapters and end-to-end tests pass against the repository's real dependencies and database migrations.
