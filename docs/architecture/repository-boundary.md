# Repository Boundary

## `machinarchy`

Owns the control plane: API, CLI, Control Manager, host inventory, workspaces,
RBAC, authoritative agent records, enrollment issuance, lease authority,
operation resources, persistence, transactional outbox and result ingestion.

## `machinarchy-agent`

Owns the distributed agent process: process lifecycle, local identity/credential
storage, outbound transport client, lease renewal client, event validation,
bounded dispatcher, local capabilities, local telemetry and host deployment.
This repository also contains the shared public protocol contracts in the
independently versioned `protocol/` Go module.

## `protocol/` nested Go module

Owns stable cross-application contracts: versioned event envelopes,
registration/lease request and response DTOs, operation DTOs, result DTOs,
subject naming and shared validation helpers. It also owns transport-neutral
execution, host-facts and service-management contracts shared by the controller
and Agent.

The protocol module is physically stored in the Agent repository to reduce
repository overhead, not because the Agent runtime owns controller policy.
It must remain dependency-light and must not import either application's
implementation packages or transport clients.

## Dependency direction

```text
machinarchy ───────────────────────┐
                                   ├──> machinarchy-agent/protocol module
machinarchy-agent internal code ───┘

machinarchy-agent cmd
    └──> internal/app
          ├──> internal/transport contracts
          └──> internal services and capabilities

internal/transport/nats implements the transport contracts.
```

Neither application imports the other's `internal/` packages. Each process
owns its own transport adapter and connection lifecycle; the two applications
do not share a NATS client wrapper. The protocol module has no application
runtime or infrastructure dependencies.

## Current Agent package ownership

The standalone Agent keeps implementation divided by responsibility:
`internal/app`, `internal/config`, `internal/controlplane`,
`internal/transport`, `internal/transport/nats`, `internal/identity`,
`internal/events`, `internal/operations`, and `internal/capabilities`.
`cmd/machinarchy-agent` is only the composition root. Do not reintroduce a
catch-all `internal/agent` package.

Do not move controller-only `internal/agentcontrol` handlers,
service/repository implementations, migrations, or outbox adapters into the
Agent repository. Those remain in the controller repository. They should
consume public contracts from the nested protocol module.
