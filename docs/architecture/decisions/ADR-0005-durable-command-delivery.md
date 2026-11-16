# ADR-0005: Durable Agent Command Delivery with JetStream

- Status: Accepted for implementation; production approval pending integration tests
- Date: 2026-11-14

## Context

The Agent previously consumed controller commands through a Core NATS
subscription. Core NATS does not retain messages for an offline Agent and does
not provide an explicit consumer acknowledgement. The local operation store
protects a command only after it reaches the Agent and has been durably
accepted. That leaves a loss window between controller publication and Agent
acceptance.

The Agent already has a persistent local operation store and result outbox.
The controller needs a durable delivery path that complements those local
safeguards without giving the Agent authority to create or modify broker
resources.

## Decision

- Use NATS JetStream for controller-to-Agent command delivery.
- Use one file-backed `WorkQueuePolicy` stream for versioned Agent event
  subjects (`machinarchy.v1.agent.*.events`).
- Provision one durable pull consumer per Agent, filtered to that Agent's exact
  event subject. Filters are disjoint by Agent ID.
- Configure explicit acknowledgement, bounded `MaxAckPending`, a finite
  `AckWait`, and unlimited redelivery (`MaxDeliver = -1`) for transient failures.
- The controller creates and validates streams/consumers. The Agent only binds
  to its pre-provisioned durable consumer and never creates or updates broker
  resources.
- The Agent acknowledges a delivery only after envelope validation, handler
  success, durable local acceptance and local event-ledger persistence.
  Invalid messages are terminated; transient local errors are negatively
  acknowledged with a bounded delay.
- The controller publishes with a stable event ID, JetStream message
  deduplication metadata and an expected-stream constraint. It records a
  command as published only after receiving a JetStream publish acknowledgement.
- Results continue to use the Agent's durable local result outbox. Controller
  result ingestion must be idempotent and must commit before publishing the
  result-acknowledgement event back through the durable Agent command stream.

## Consequences

- Delivery is **at least once**, not exactly once. The Agent's operation store,
  event ledger and operation IDs suppress duplicate dispatch after redelivery.
- A crash after an external side effect but before durable completion may still
  produce `unknown`; the Agent must not blindly repeat that operation.
- `WorkQueuePolicy` requires that consumers do not have overlapping subject
  filters. The per-Agent exact-subject filters are disjoint by design; any new
  consumer must preserve this invariant.
- Broker stream and consumer provisioning is an explicit controller
  responsibility. An offline Agent's consumer must not be deleted as part of
  ordinary liveness cleanup.
- Stream limits are bounded. When the stream reaches its limits, the controller
  must retain commands in its database outbox and retry after capacity returns;
  it must not mark the command dispatched without a JetStream publish ack.
- Production readiness still requires live NATS/JetStream integration tests,
  controller storage integration, credential/subject ACL verification, and
  crash/reconnect failure tests.
