# Migration from the Machinarchy Monolith

This guide separates agent-owned implementation from controller-owned
implementation. It is not a blind directory copy: imports, build workflows,
protocol dependencies and authority boundaries must be updated.

## Move to `machinarchy-agent`

- `cmd/agent` -> `cmd/machinarchy-agent`
- Agent-owned code from the former monolith `internal/agent/**` into the
  standalone repository's responsibility-based packages:
  `internal/app`, `internal/config`, `internal/controlplane`,
  `internal/transport`, `internal/transport/nats`, `internal/identity`,
  `internal/events`,
  `internal/operations`, and `internal/capabilities`
- Agent-specific tests alongside the package they verify
- Agent documentation and deployment files
- Agent binary build/release steps

Imports must target the owning package in this repository; there is no
replacement catch-all `internal/agent` import path. Replace the monolith's
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
