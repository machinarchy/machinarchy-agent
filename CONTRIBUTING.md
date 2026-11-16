# Contributing

## Before opening a pull request

1. Read `AGENTS.md`, `protocol/AGENTS.md` and the architecture documents.
2. Keep Agent runtime changes within the Agent responsibility boundary.
3. Keep shared wire-contract changes inside `protocol/`; do not duplicate DTOs
   in the Agent or controller application packages.
4. Add or update tests for all behavior and contract changes.
5. Update `ChangeLogAI.md` for every repository change.
6. Update protocol compatibility documentation when wire behavior changes.
7. Run `make check` from the repository root.

## Go conventions

- Keep transport code separate from lifecycle and capability behavior.
- Prefer small functions and explicit dependencies.
- Return errors with context; do not hide failed cleanup.
- Use structured `log/slog` fields.
- Use bounded queues and context-aware operations.
- Do not use package-global mutable runtime state.
- Do not expose secrets in error messages or test output.

## Protocol changes

Protocol changes are made in the `protocol/` nested Go module in this same
repository. Keep the module dependency-light and transport-neutral. Update
Agent contract tests and coordinate the import-path/module-version migration
with the `machinarchy` controller repository. Breaking wire changes require an
explicit versioning decision and a rollout/compatibility plan.

The protocol module uses Git tags prefixed with `protocol/`, for example
`protocol/v0.1.0`. Agent releases use ordinary root-level SemVer tags.
Do not mix these version streams.

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
