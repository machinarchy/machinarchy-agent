# Protocol Compatibility

The Agent imports the public `github.com/machinarchy/machinarchy-protocol`
module. The current wire schema has `ProtocolVersion = 1` and is still
provisional until controller/agent end-to-end tests are complete.

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
per-agent result subjects. It also defines transport-neutral execution payloads
in `execution/v1`; both SSH and Agent executors must preserve those payload and
result semantics. NATS credentials must be restricted to the required subjects.
The agent must never subscribe to a wildcard covering other agents.

## Controller integration gate

Before deploying an Agent release:

1. Verify the controller dependency resolves to the same protocol module version.
2. Verify controller request/reply handlers serialize matching response DTOs.
3. Verify controller publishes to the exact per-agent event subject.
4. Verify result consumer validates agent identity and persists the result.
5. Run full NATS end-to-end tests with production-equivalent ACLs.
