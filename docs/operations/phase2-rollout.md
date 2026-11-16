# Phase 2 rollout gate: durable command delivery

The Agent now binds to a controller-provisioned NATS JetStream durable pull
consumer. This improves delivery while the Agent is offline, but the overall
controller/Agent path must be integrated and tested before production use.

## Required controller work

1. Pin the same nested module release from
   `github.com/machinarchy/machinarchy-agent/protocol` in the controller and
   Agent. The wire protocol envelope remains version 2. The `hostfacts/v1` and
   `services/v1` schemas are additive and do not bump that envelope version.
2. Create the `MACHINARCHY_AGENT_EVENTS` file-backed stream (or an explicitly
   configured equivalent) with subject `machinarchy.v1.agent.*.events`,
   `WorkQueuePolicy`, bounded message/byte/age limits and `DiscardNewPolicy`.
3. After Agent enrollment, create a durable pull consumer named
   `agent_<agent-id>` with an exact filter
   `machinarchy.v1.agent.<agent-id>.events`, explicit acknowledgements and
   bounded outstanding deliveries.
4. Publish commands from the controller's durable DB outbox using stable event
   IDs, JetStream message IDs and expected-stream checks. Mark the outbox row as
   published only after receiving a valid JetStream publish acknowledgement.
5. Subscribe to each Agent's result subject (or a wildcard protected by
   authentication and ACLs). Derive the Agent identity from the authenticated
   broker principal, verify it matches the result subject and body, and never
   trust the self-asserted `agent_id` alone.
6. Persist result updates idempotently by `(agent_id, operation_id)`. Reject
   conflicting terminal outcomes. Only after the database transaction commits,
   publish `operation.result.acknowledged` to that Agent's durable command
   subject.
7. Treat `accepted` and `running` as progress updates. Do not send result
   acknowledgements for them: the Agent only keeps terminal outcomes in its
   durable result outbox.
8. Preserve durable consumers when an Agent is temporarily offline. Delete a
   consumer only during controlled decommissioning after pending work is
   handled.

The `controller-overlay/internal/agentdelivery` package in the companion
controller overlay ZIP provides stream/consumer provisioning, durable event
publishing and result persistence/acknowledgement orchestration interfaces. It
must be adapted to the controller's actual store and broker packages.

## Required end-to-end tests

- Publish a command while the Agent is offline; reconnect and verify the
  durable consumer delivers it.
- Publish a command and terminate the Agent after delivery but before local
  acceptance; verify JetStream redelivers it.
- Terminate the Agent after durable local acceptance but before message ack;
  verify redelivery does not execute the operation twice.
- Restart the Agent after its durable `running` transition; verify the result
  becomes `unknown` and no automatic second execution occurs.
- Fill the Agent's local queue; verify it records a durable `rejected` terminal
  result before acknowledging the JetStream command.
- Make controller storage unavailable; verify no result acknowledgement is
  published and the Agent retains/retries its terminal result.
- Commit a controller result but drop the ack event; verify result redelivery
  is idempotent and the controller publishes the same stable acknowledgement
  event ID again.
- Disconnect the broker during publication; verify the controller keeps its
  database outbox item pending unless JetStream confirms publication.
- Verify Agent NATS credentials can pull only their own durable consumer and
  publish only to their own results subject; verify they cannot consume other
  Agents' commands or administer streams/consumers.

## Delivery semantics and limits

JetStream provides durable, explicit-ack message delivery. The application
contract is still at-least-once. A publish acknowledgement confirms JetStream
accepted a command; it does not mean the Agent executed it. An Agent ack means
the event handler completed its local durable acceptance path; it does not mean
the external operation succeeded. Operation results are the authoritative
execution outcome.

Do not claim exactly-once execution for arbitrary commands, scripts or external
side effects. A crash can occur after a side effect and before the Agent saves
the terminal result. That case is represented as `unknown` and requires
operator/controller policy rather than blind automatic retry.

The isolated tests use a compile-time JetStream API stub. They do not replace
verification with the real nats.go v1.55.0 client, a live nats-server with
JetStream enabled, or a production-equivalent controller/Agent deployment.
