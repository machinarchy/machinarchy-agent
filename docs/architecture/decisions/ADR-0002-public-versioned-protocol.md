# ADR-0002: Shared Versioned Protocol Module

- Status: Superseded by ADR-0006
- Date: 2026-11-14

## Context

The controller and agent must serialize identical messages. Copying DTOs into
both repositories creates drift, and importing the controller's `internal/`
packages is not permitted by Go's visibility rules or by the desired boundary.

## Original decision

Publish transport-neutral wire contracts in a separate
`github.com/machinarchy/machinarchy-protocol` Go module. The module had a
versioned `agent/v1` package and did not depend on NATS or application code.

## Historical consequences

- Controller and agent upgraded the protocol module explicitly.
- Breaking wire changes required a compatibility plan.
- Subject permissions and payload validation remained responsibilities of the
  broker/controller/agent integration, not the protocol types alone.

## Supersession

ADR-0006 keeps the same independent public module boundary but relocates its
source into `protocol/` inside the `machinarchy-agent` repository. It changes
the Go module import path and repository hosting arrangement; it does not
change the wire contract solely by virtue of that relocation.
