# Agent Architecture

## Purpose

The Machinarchy Agent is a lightweight, long-running, outbound-connected
Linux OS-management runtime. It reports selected host facts and, when locally
configured, inspects or controls allowlisted systemd services. It is not a
general-purpose remote shell, controller, scheduler, inventory database, or
source of authoritative identity.

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
6. **Capability registry** — resolves only the supported, explicitly
   registered OS-management capabilities; generic command and script execution
   are not part of the Agent product profile.
7. **Local state** — persists the controller-assigned identity, bounded event
   ledger and durable operation lifecycle with private filesystem permissions.
8. **Host facts** — exposes a small read-only `system.facts` capability for
   inventory refresh. Platform-specific metrics are best-effort and omitted
   when unavailable; sensitive host details are intentionally excluded.
9. **Service operations** — exposes opt-in `service.status` and
   `service.control` capabilities for exact, locally allowlisted Linux/systemd
   units. Lifecycle actions require a separate local opt-in.

## Go package map

The standalone Agent repository keeps implementation responsibilities in
separate packages rather than a catch-all `internal/agent` package:

| Package | Responsibility |
| --- | --- |
| `cmd/machinarchy-agent` | Process bootstrap and dependency composition only |
| `internal/app` | Runtime lifecycle and Agent session orchestration |
| `internal/config` | Configuration loading, defaults and validation |
| `internal/controlplane` | Registration and lease request/reply contracts and client |
| `internal/transport` | Narrow connection, connector and lifecycle-event contracts |
| `internal/transport/nats` | NATS connection, JetStream subscription and publishing adapter |
| `internal/identity` | Agent identity model and local credential persistence |
| `internal/events` | Event validation, deduplication and event-ledger persistence |
| `internal/operations` | Bounded operation dispatch, cancellation and durable operation state |
| `internal/capabilities` | Explicitly registered OS-management capabilities and platform adapters |

`internal/app` consumes the narrow contracts in `internal/transport`; the
concrete NATS adapter implements those contracts without importing the app
package. The command entry point wires the adapter into the runtime. Tests live
beside the package whose behavior they verify.

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

The process initiates outbound NATS connections. Registration and lease
renewal use Core NATS request/reply. Controller-to-Agent commands use a
controller-provisioned JetStream durable pull consumer, filtered to the exact
Agent event subject and configured for explicit acknowledgements. Agent results
are published over Core NATS and retried from the Agent's durable result outbox
until the controller acknowledges durable persistence through the JetStream
command path.

The controller owns stream/consumer provisioning. The Agent only binds to its
existing durable consumer. The command stream uses disjoint per-Agent subject
filters so WorkQueue retention can be used without overlapping consumers.
Production requires `tls://` endpoints, NATS authentication and strict
subject-level permissions for the exact Agent consumer and result subject.

Delivery is at-least-once, not exactly-once. The Agent acknowledges an inbound
event only after local durable handling, and a controller result acknowledgement
is sent only after durable result persistence. A process crash after an
external side effect may still produce `unknown`; that outcome must not trigger
blind automatic execution retries.

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
runtime metadata plus the registered capability names. `system.facts` is also
read-only and returns a bounded, privacy-conscious host snapshot; it does not
collect IP addresses, usernames, environment variables, process lists, mount
lists or machine IDs. `service.status` and `service.control` are separately
opt-in, Linux/systemd-specific capabilities backed by shared `services/v1`
contracts. The Agent does not expose generic remote command execution and does
not implement SSH. See
[`../operations/phase5-service-management.md`](../operations/phase5-service-management.md).
