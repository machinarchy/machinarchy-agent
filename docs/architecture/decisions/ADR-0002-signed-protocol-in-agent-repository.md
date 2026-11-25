# ADR-0002: Signed protocol owned by the agent repository

- Status: Accepted
- Date: 2026-11-17
- Supersedes: the previous ADR-0002 (separate `machinarchy-protocol` module),
  ADR-0004 (separate durable operation store), ADR-0005 (durable command
  delivery over the controller namespace)

## Context

The agent depended on a `machinarchy-protocol` module that did not exist, so
it could not be built. Its subjects lived under the controller's
`machinarchy.>` stream namespace. Command authenticity rested entirely on broker
ACLs, which the controller could not even configure. Agent state was split
across three files that were updated separately.

## Decision

1. **The contract lives in `protocol/agentv1` in this repository.** It depends
   only on the standard library, and the controller imports it. If a separate
   module is ever needed, the package moves without changing its API.
2. **Subjects live under `machinarchy-agent.v1`**, outside the controller's
   internal namespace.
3. **Every message is an Ed25519-signed envelope.**
   - The controller signs commands and responses, and agents pin its key.
   - Agents sign their requests and results with a key they generate and
     register with proof of possession.
   - No bearer lease token exists.
4. **The agent keeps all durable state in one atomically replaced file.**

## Consequences

- Authenticity no longer depends on broker ACLs alone. ACLs are still needed
  for confidentiality and to prevent denial of service.
- The controller must implement the flows in `docs/protocol.md`.
  `internal/fakecontroller` is a reference implementation.
- Rotating the controller key means redeploying the public key to agents.
