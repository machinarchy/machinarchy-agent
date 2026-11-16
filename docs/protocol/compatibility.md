# Protocol Compatibility

The Agent imports the public `github.com/machinarchy/machinarchy-protocol`
module. This change introduces Agent protocol version 2. Version 2 adds the
`unknown` operation result status and the
`operation.result.acknowledged` event used to release durable result outbox
entries. A version-1 controller must not be treated as compatible with this
Agent build.

## Compatibility policy

- Go module semver and wire protocol version are separate version axes.
- A Go module patch/minor release may add non-breaking constants or helper
  functions without changing serialized messages.
- Additive optional JSON fields may be compatible only when both sides tolerate
  unknown/missing fields as designed; verify with strict decoder behavior.
- Changing required fields, semantics, event names, NATS subjects or status
  values is potentially breaking.
- A breaking contract needs an explicit version negotiation and rollout plan.
- Controller and agent releases must publish a supported compatibility matrix.

## Subject contract

The protocol module defines the registration, lease-renewal, per-agent event and
per-agent result subjects. It also retains transport-neutral execution payloads
in `execution/v1` for the controller's independent SSH transport. The current
Agent does not register or execute `exec.command` or `exec.script`; its supported
capabilities are the OS-management operations documented in this repository.
NATS credentials must be restricted to the required subjects. The Agent must
never subscribe to a wildcard covering other agents.

## Controller integration gate

Before deploying an Agent release:

1. Verify the controller dependency resolves to the same protocol module version.
2. Verify controller request/reply handlers serialize matching response DTOs.
3. Verify controller publishes to the exact per-agent event subject.
4. Verify the result consumer validates Agent identity and durably upserts by
   `operation_id` before publishing `operation.result.acknowledged`.
5. Verify controller state machines accept the `unknown` operation status and
   never treat it as a safe automatic retry.
6. Provision the JetStream command stream and one exact-subject durable pull
   consumer per Agent. The Agent binds to, but does not create, broker resources.
7. Publish controller outbox items with stable event IDs and only mark them
   published after a valid JetStream publish acknowledgement.
8. Run full NATS/JetStream end-to-end tests with production-equivalent ACLs.
