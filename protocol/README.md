# Machinarchy Protocol Contracts

This directory is the public, transport-neutral contract module used by the
Machinarchy Agent and the Machinarchy controller. It lives in the
`machinarchy-agent` Git repository to reduce repository overhead, but remains
an independently versioned Go module:

```go
import agentv1 "github.com/machinarchy/machinarchy-agent/protocol/agent/v1"
```

## Packages

- `agent/v1`: registration, lease, event-envelope, operation and result wire
  contracts; protocol version constants; subject and consumer naming helpers.
- `execution/v1`: transport-neutral command, script and result contracts used
  by controller-side SSH and Agent execution paths.
- `hostfacts/v1`: bounded, privacy-conscious host inventory schema and strict
  decoder/validator.
- `services/v1`: service status/control contract types and input validators.

## Boundaries

This module must remain dependency-light and transport-neutral. Do not import
NATS clients, database drivers, HTTP frameworks, SSH implementations, Agent
runtime packages or controller internals. JSON names, enum values, validation
rules and subject formats are compatibility-critical APIs.

The contracts are shared source code, not authorization policy. The controller
remains authoritative for identity, leases, workspaces and operation
permissions. The Agent remains responsible for enforcing its local capability
allowlist and host privilege boundary.

## Versioning

The Go module version and wire protocol version are separate. `agent/v1`
currently declares wire `ProtocolVersion = 2`; moving this module into the Agent
repository does not change that wire version or any serialized contract.

Because the module is nested under `protocol/`, publish module releases using
prefixed Git tags, for example:

```bash
git tag protocol/v0.1.0
git push origin protocol/v0.1.0
```

The root Agent module continues to use ordinary `vX.Y.Z` tags. Do not use a
root Agent release tag as a protocol module version.

The new module path is `github.com/machinarchy/machinarchy-agent/protocol`.
Consumers outside this repository (including `machinarchy`) must update their
imports and `go.mod` dependency to this path. The first release under the new
module path starts at `v0.1.0`; this is a Go module path migration only and does
not imply a wire protocol v1 release.

See [execution contract semantics](docs/execution-contracts.md) and
[protocol compatibility](docs/compatibility.md) for controller rollout and
execution-specific details.

## Validation

Run the protocol module checks from this directory:

```bash
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
```

The root repository CI runs these checks alongside Agent checks.
