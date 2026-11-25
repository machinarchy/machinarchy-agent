# Contributing

## Before opening a pull request

1. Read `AGENTS.md` and the architecture documents.
2. Keep changes within the agent's responsibility boundary.
3. Add or update tests for all behavior changes.
4. Update `ChangeLogAI.md` for every repository change.
5. Update `docs/protocol.md` and the golden fixtures when wire behavior changes.
6. Run `make check`.

## Go conventions

- Keep transport code separate from lifecycle and capability behavior.
- Prefer small functions and explicit dependencies.
- Return errors with context; do not hide failed cleanup.
- Use structured `log/slog` fields.
- Use bounded queues and context-aware operations.
- Do not use package-global mutable runtime state.
- Do not expose secrets in error messages or test output.

## Protocol changes

Protocol changes are made in `protocol/agentv1`; coordinate them with the controller and bump `ProtocolVersion` for incompatible changes.
Update both controller and agent compatibility tests. Breaking changes require
an explicit versioning decision and a rollout/compatibility plan. Never change
wire payloads without updating the controller in the same release plan.

## Capability contributions

Every capability must define:

- a stable capability name and version;
- a strict input schema and maximum input size;
- explicit authorization requirements from the operation contract;
- a bounded timeout and output size;
- context cancellation behavior;
- sensitive-data redaction rules;
- unit tests for valid, invalid, cancelled and timed-out inputs.

Unrestricted shell or script execution is not an acceptable default capability.
