# Agent Lifecycle

## Startup

1. Load and validate environment configuration.
2. Create a JSON `slog` logger.
3. Open the local identity store, event ledger and durable operation store.
4. Establish the outbound NATS connection.
5. Load the controller-assigned identity or enroll using the one-time token.
6. Establish/renew the lease before accepting events.
7. Bind to the controller-provisioned durable JetStream consumer filtered to
   this Agent's event subject.
8. Start the bounded operation dispatcher.
9. Recover safe-to-resume accepted operations and queue pending result publications.
10. Process events while renewing the lease on the controller-provided interval.

## Broker reconnect

On disconnection, pause event delivery and wait for NATS to reconnect. Once
connected, renew the lease and update the event processor's identity/lease
snapshot before resuming processing. A lease that has expired must not be
assumed valid just because the socket reconnects.

## Normal shutdown

On SIGTERM/SIGINT:

1. Stop accepting new event messages.
2. Close/unsubscribe the inbound event subscription.
3. Drain accepted operations for the configured drain window.
4. Cancel remaining work and wait for bounded cleanup grace.
5. Drain and close the NATS connection.

## Fatal shutdown

Unexpected session failure, permanent authorization rejection, or lease loss
must not allow potentially unauthorized work to continue. Abort accepted work,
close transport resources and return an error for process supervision.

## Durable operation state and crash recovery

The Agent persists operation acceptance before enqueueing it and persists the
`running` transition before calling a capability. When the durable state moves
to `running`, raw request and event payloads are removed from the snapshot; a
request digest remains for duplicate-ID detection. Terminal results are retained
until controller acknowledgement, after which result and input payloads are
cleared while the compact deduplication tombstone remains. The default local
store is
`<identity-file>.operations.json` (override with
`MACHINARCHY_AGENT_OPERATION_STORE_FILE`). Its file mode is `0600`; its parent
directory must be `0700`. The snapshot is bounded and atomically replaced with
file and directory synchronization. Run one Agent process per state directory.

On restart:

- `accepted` operations that have not expired may be resumed because the Agent
  recorded that execution had not started. If the recovered accepted backlog is
  larger than the configured queue capacity, startup fails with an actionable
  error rather than deadlocking or silently dropping accepted work. Restore or
  increase `MACHINARCHY_AGENT_OPERATION_QUEUE_SIZE`; do not delete the store.
- `running` operations become `unknown`. A `cancel_requested` operation with
  no persisted `started_at` becomes `cancelled`, because the Agent records the
  running transition before invoking a capability. A cancellation after
  `started_at` remains `unknown`, because an external side effect may already
  have happened. Neither `running` nor started cancellation records are
  automatically re-executed.
- terminal results remain in the local outbox and are republished until the
  controller acknowledges durable persistence.
- acknowledged operation IDs remain as compact deduplication tombstones until
  their event expiry, then become eligible for bounded-history pruning.

The result acknowledgement is an `operation.result.acknowledged` Agent event.
The controller must send it only after committing the result to its own durable
store. It must deduplicate results by `operation_id`, because publication can
be repeated after reconnects or ambiguous publish outcomes. Unacknowledged
results are bounded; once the Agent reaches its pending-operation capacity, it
rejects additional work rather than growing local state without limit.

## Delivery guarantee boundary

Inbound command delivery uses a JetStream durable pull consumer with explicit
acknowledgements. The controller must provision the stream and per-Agent
consumer, and publish only from its durable DB outbox. The Agent acknowledges a
command after local durable handling and event-ledger persistence. Results are
published over Core NATS from the Agent's durable result outbox and retried
until the controller sends an acknowledgement through the JetStream command
stream after committing the result.

The overall delivery model is at-least-once, not exactly-once. A crash after an
external side effect but before a terminal result is durable may still produce
`unknown`. The controller must treat `unknown` as requiring operator/policy
resolution, not automatic fallback to SSH.
