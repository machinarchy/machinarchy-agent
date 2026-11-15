# Agent Architecture

## Purpose

The Machinarchy Agent is a long-running, outbound-connected runtime installed
on managed hosts. It is a capability adapter and execution endpoint. It is not
a controller, scheduler, inventory database, or source of authoritative
identity.

## Runtime layers

1. **Process bootstrap** — reads configuration, initializes structured logging
   and signal-aware process context.
2. **Transport adapter** — owns NATS connection, authentication, reconnection
   notifications, bounded subscriptions, publishing and graceful drain.
3. **Agent session** — enrolls or loads assigned identity, renews the lease,
   and coordinates the subscription/dispatcher lifecycle.
4. **Event validation** — validates the versioned envelope, target agent/host,
   event type, timestamps, payload size and deduplication identifier.
5. **Operation dispatcher** — enforces bounded queue depth, bounded worker
   count, operation deadlines and cancellation.
6. **Capability registry** — resolves only explicitly registered capabilities.
   Command and script capabilities are opt-in, validated, bounded and
   cancellable; they are never enabled by default.
7. **Execution adapter** — maps transport-neutral execution contracts to local
   process execution, preserving stdout, stderr and exit status.
8. **Local state** — persists the controller-assigned identity and bounded
   event ledger with private filesystem permissions.

## Authority boundaries

The controller is authoritative for enrollment, agent ID, host association,
lease validity, workspace authorization, operation scheduling and operation
state. The agent stores the last controller-issued identity and lease
credential only to authenticate renewals; it cannot mint or extend its own
lease.

The controller's `internal/` packages are not imported by this repository.
The only cross-repository Go dependency for application-level messages is the
public `machinarchy-protocol` module.

## Transport

The initial adapter uses NATS Core. The process initiates outbound connections.
Production configuration requires `tls://` endpoints and NATS authentication.
The agent should receive subject-level permissions for registration/renewal,
its own event subject, and its own result subject only.

Core NATS does not provide durable replay or explicit acknowledgements. The
current bounded local ledger is not a durable queue and does not create an
exactly-once guarantee.

## Capability safety

Capabilities are registered in code and use a strict input/output contract.
Every capability must:

- validate input before work;
- observe context cancellation;
- stay within a configured deadline;
- bound output size;
- avoid writing secret values to logs or results;
- expose a stable, documented capability name and version.

The built-in `runtime.info` capability is read-only and reports limited
runtime metadata plus the configured capability names. `exec.command` and
`exec.script` are available only when `MACHINARCHY_AGENT_EXECUTION_ENABLED=true`.
They use the common `machinarchy-protocol/execution/v1` contracts also intended
for the controller's SSH executor. The Agent itself does not implement SSH.
