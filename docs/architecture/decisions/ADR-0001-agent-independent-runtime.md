# ADR-0001: Agent as an Independent Runtime

- Status: Proposed
- Date: 2026-11-14

## Context

The agent executes capabilities on managed hosts and has a lifecycle, security
boundary, deployment model, version cadence and resource profile distinct from
the central control plane. Keeping it in the controller repository couples
host-level runtime changes to API/database/controller releases.

## Decision

Maintain `machinarchy-agent` as an independently built, tested, versioned and
deployed Go application. The controller remains the authority for enrollment,
leases, authorization, scheduling and operation state.

## Consequences

- Agent CI, binary releases, container image and systemd packaging are owned by
  the agent repository.
- The agent must not import controller `internal/` packages.
- Cross-repository communication uses the contract in `protocol/agentv1` (see ADR-0002).
- Compatibility tests and a coordinated rollout policy are required.
- Repository separation does not, by itself, provide runtime isolation or
  eliminate the need for secure NATS credentials and subject ACLs.
