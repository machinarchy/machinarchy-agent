# ADR-0002: Shared Versioned Protocol Module

- Status: Proposed
- Date: 2026-11-14

## Context

The controller and agent must serialize identical messages. Copying DTOs into
both repositories creates drift, and importing the controller's `internal/`
packages is not permitted by Go's visibility rules or by the desired boundary.

## Decision

Publish transport-neutral wire contracts in the separate
`github.com/machinarchy/machinarchy-protocol` Go module. The module has a
versioned `agent/v1` package and does not depend on NATS or application code.

## Consequences

- Controller and agent upgrade the protocol module explicitly.
- Breaking wire changes require a new protocol major version or an explicitly
  negotiated compatibility transition.
- Subject permissions and payload validation remain responsibilities of the
  broker/controller/agent integration, not the protocol types alone.
- The initial v1 wire contract remains provisional until reconciled with the
  controller's accepted ADRs and real end-to-end tests.
