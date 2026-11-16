# Protocol Compatibility

The Agent imports the nested public module
`github.com/machinarchy/machinarchy-agent/protocol`. Its source lives in this
repository under `protocol/`, but it remains an independently versioned Go
module for controller consumption. The current wire protocol version is 2.
Version 2 adds the `unknown` operation result status and the
`operation.result.acknowledged` event used to release durable result outbox
entries. A version-1 controller must not be treated as compatible with this
Agent build.

## Module and wire versioning

- Go module semver and wire protocol version are separate axes.
- The nested module is released with `protocol/vX.Y.Z` Git tags; Agent releases
  use root-level `vX.Y.Z` tags.
- Moving the module to its new import path does not change JSON payloads or the
  wire `ProtocolVersion`.
- The new module path starts at `v0.1.0`; this Go module version is not wire
  protocol version 1.
- A Go module patch/minor release may add non-breaking constants or helper
  functions without changing serialized messages.
- Additive optional JSON fields may be compatible only when both sides
  tolerate unknown/missing fields as designed; verify strict decoder behavior.
- Changing required fields, semantics, event names, NATS subjects or status
  values is potentially breaking.
- A breaking contract needs an explicit version negotiation and rollout plan.
- Controller and Agent releases must publish a supported compatibility matrix.

## Subject contract

The protocol module defines registration, lease-renewal, per-Agent event and
per-Agent result subjects. It also retains transport-neutral execution payloads
in `execution/v1` for the controller's independent SSH transport. The current
Agent does not register or execute `exec.command` or `exec.script`; its
supported capabilities are the OS-management operations documented in this
repository. NATS credentials must be restricted to the required subjects. The
Agent must never subscribe to a wildcard covering other agents.

## Controller migration required

Before archiving the former `machinarchy-protocol` repository, update the
controller repository:

1. Replace imports from
   `github.com/machinarchy/machinarchy-protocol/...` with
   `github.com/machinarchy/machinarchy-agent/protocol/...`.
2. Require `github.com/machinarchy/machinarchy-agent/protocol v0.1.0` or a later
   prefixed-tag release.
3. Remove the old protocol module dependency. If this repository is private,
   configure read-only access to `github.com/machinarchy/machinarchy-agent`
   (for example, `GOPRIVATE=github.com/machinarchy/machinarchy-agent`) in the
   controller's module-download environment.
4. Run controller serialization/contract tests and real controller-Agent
   integration tests before release.

The old repository must not be deleted before this migration is complete.

## Controller integration gate

Before deploying an Agent release:

1. Verify the controller dependency resolves to the same protocol module
   version.
2. Verify controller request/reply handlers serialize matching response DTOs.
3. Verify the controller publishes to the exact per-Agent event subject.
4. Verify the result consumer validates Agent identity and durably upserts by
   `operation_id` before publishing `operation.result.acknowledged`.
5. Verify controller state machines accept `unknown` and never treat it as a
   safe automatic retry.
6. Provision the JetStream command stream and one exact-subject durable pull
   consumer per Agent. The Agent binds to, but does not create, broker resources.
7. Publish controller outbox items with stable event IDs and only mark them
   published after a valid JetStream publish acknowledgement.
8. Run full NATS/JetStream end-to-end tests with production-equivalent ACLs.
