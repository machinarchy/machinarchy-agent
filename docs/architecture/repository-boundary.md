# Repository Boundary

## `machinarchy`

Owns the control plane: API, CLI, Control Manager, host inventory, workspaces,
RBAC, authoritative agent records, enrollment issuance, lease authority,
operation resources, persistence, transactional outbox and result ingestion.

## `machinarchy-agent`

Owns the distributed agent process: process lifecycle, local identity/credential
storage, outbound transport client, lease renewal client, event validation,
bounded dispatcher, local capabilities, local telemetry and host deployment.

## `machinarchy-protocol`

Owns stable cross-repository wire contracts: versioned event envelopes,
registration/lease request and response DTOs, operation DTOs, result DTOs,
subject naming and shared validation helpers.

## Dependency direction

```text
machinarchy ───────────────┐
                           ├──> machinarchy-protocol
machinarchy-agent ─────────┘
```

Neither application imports the other's `internal/` packages. Each process
owns its own transport adapter and connection lifecycle; the two applications
do not need to share a NATS client wrapper. The protocol module must not depend
on a NATS client, database, Echo, SSH library, or either application's internal
code.

## Moving the code from the monolith

Move agent-owned code from `cmd/agent` and `internal/agent` into this
repository. Do not move controller-only `internal/agentcontrol` handlers,
service/repository implementations, migrations, or outbox adapters into the
agent repository. Those belong in the controller repository and should import
the shared protocol module.
