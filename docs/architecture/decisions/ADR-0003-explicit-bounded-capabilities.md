# ADR-0003: Explicit and Bounded Capabilities

- Status: Proposed
- Date: 2026-11-14

## Context

The agent receives remotely supplied messages and executes work on managed
machines. An unrestricted shell endpoint would turn any controller credential,
broker ACL or validation defect into broad remote code execution.

## Decision

Operations resolve only to capabilities explicitly registered by the agent.
Each capability validates input, enforces authorization information from the
controller contract, observes context cancellation, and bounds time, memory,
output and concurrency. Arbitrary shell/script execution is not enabled by
default.

## Consequences

- Every capability requires tests and documentation.
- New capabilities require explicit security review.
- Capability identity and versioning become part of the compatibility contract.
- The controller remains responsible for deciding who may request each
  operation; the agent performs defense-in-depth validation.
