# Migration from the Machinarchy Monolith

This guide separates agent-owned implementation from controller-owned
implementation. It is not a blind directory copy: imports, build workflows,
protocol dependencies and authority boundaries must be updated.

## Move to `machinarchy-agent`

- `cmd/agent` -> `cmd/machinarchy-agent`
- `internal/agent/**` -> `internal/agent/**`
- Agent-specific runtime/session/identity/event/dispatcher tests
- Agent documentation and agent deployment files
- Agent binary build/release steps

Update imports from the old monolith module path to
`github.com/machinarchy/machinarchy-agent/internal/agent`, and replace the old
internal event contract import with
`github.com/machinarchy/machinarchy-protocol/agent/v1`.

## Move to `machinarchy-protocol`

Move the transport-neutral contract sources from the monolith's
`internal/eventing/agentv1` package into the public `agent/v1` package in the
protocol repository. Both applications import this module.

## Keep in `machinarchy`

Do not move these controller responsibilities:

- `internal/agentcontrol` handlers and publisher adapters;
- database records, migrations and repositories for agents/leases/operations;
- enrollment token issuance and validation;
- authoritative lease state and revocation;
- operation scheduling, authorization, history and cancellation authority;
- transactional outbox and broker consumer/publisher wiring;
- API/CLI/Control Manager integration.

Adapt controller-side handlers to the public protocol module and existing
service/store/broker contracts. The `internal/agentcontrol` code from the
Phase 6 reference bundle is controller-side integration material, not code for
this repository.

## Update the controller repository

1. Add `github.com/machinarchy/machinarchy-protocol` as a dependency.
2. Replace the controller's private `internal/eventing/agentv1` definitions
   only after confirming the protocol contract against accepted ADRs.
3. Keep existing controller broker connection management; do not create a
   second NATS manager.
4. Implement registration and lease handlers against the authoritative store.
5. Ensure registration is idempotent by stable `request_id`.
6. Enforce broker ACLs for registration, renewal, agent-specific events and
   results.
7. Persist operation results and deduplicate by operation/attempt identity.
8. Publish operations through the controller's transactional outbox.
9. Add real NATS and full controller -> agent -> controller integration tests.

## Remove from controller release workflow

Remove the `./cmd/agent` build step from the controller release workflow after
this new repository has an independently verified release pipeline. Keep the
controller and agent release schedules independent; document supported
controller/agent/protocol compatibility combinations.

## Rollout order

1. Publish and verify the protocol module.
2. Deploy controller support for that protocol version.
3. Publish the agent binary/container using the same protocol version.
4. Enroll a non-production test host and verify renewal/event/result lifecycle.
5. Roll out to a small canary group before wider deployment.
