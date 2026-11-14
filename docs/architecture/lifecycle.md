# Agent Lifecycle

## Startup

1. Load and validate environment configuration.
2. Create a JSON `slog` logger.
3. Open the local identity store and event ledger.
4. Establish the outbound NATS connection.
5. Load the controller-assigned identity or enroll using the one-time token.
6. Establish/renew the lease before accepting events.
7. Subscribe to the agent-specific event subject.
8. Start the bounded operation dispatcher.
9. Process events while renewing the lease on the controller-provided interval.

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

## Crash recovery limitation

The current dispatcher queue and in-flight operation state are in memory. A
process crash may lose accepted but incomplete work or an unpublished result.
A durable operation journal and result outbox/acknowledgement protocol are
required before claiming restart-safe execution.
