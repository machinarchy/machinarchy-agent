# ADR-0006: Keep Shared Protocol Module in the Agent Repository

- Status: Accepted
- Date: 2026-11-16
- Supersedes: ADR-0002's separate-repository hosting decision

## Context

Machinarchy has three repositories: the controller, the Agent and a small
protocol-only repository. The protocol contracts are shared by the controller
and Agent, but the standalone repository adds another issue tracker, CI
workflow, permissions boundary and coordinated source checkout. The protocol
must remain independently consumable by the controller and must not become
coupled to Agent runtime dependencies.

The protocol currently contains `agent/v1`, `execution/v1`, `hostfacts/v1` and
`services/v1`. In particular, `execution/v1` is also used by controller-side
SSH execution and is not exclusively an Agent concern.

## Decision

Move the protocol source into `protocol/` inside the `machinarchy-agent`
repository, but preserve it as a separate nested Go module. Its module path is
`github.com/machinarchy/machinarchy-agent/protocol`.

The Agent root module uses a local `replace` directive to develop and test
against the bundled protocol source. The controller imports only the nested
protocol module and does not depend on the Agent root module.

Protocol releases use `protocol/vX.Y.Z` Git tags. Agent releases continue to
use root-level `vX.Y.Z` tags. The Go module version and wire `ProtocolVersion`
remain independent version axes.

## Consequences

- One GitHub repository owns the Agent runtime and the shared contract source.
- The controller no longer needs access to a separate protocol repository.
- The protocol remains an independently versioned Go module with a small
  dependency graph.
- The protocol module path changes. The controller must update imports and
  its `go.mod` before the old repository can be archived.
- A new module path starts at `v0.1.0`; this does not change the wire protocol
  version or serialized contract.
- Root Agent CI must explicitly run tests, race tests and vet inside the nested
  module because `go test ./...` at the root does not traverse nested modules.
- Publishing a protocol update requires a prefixed Git tag and a controller
  dependency update; an Agent release tag alone does not publish a protocol
  module version.
