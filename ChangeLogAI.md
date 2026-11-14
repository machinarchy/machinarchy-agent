# AI Change Log

## 2026-11-14 — Initial repository extraction baseline

### Added

- Extracted agent runtime source and tests from the Machinarchy monolith into
  `internal/agent`.
- Added the standalone `cmd/machinarchy-agent` entry point.
- Moved shared agent v1 wire contracts to the public
  `machinarchy-protocol/agent/v1` module.
- Added repository governance, CI/release workflows, Docker and systemd
  deployment files, configuration examples, and architecture/security docs.

### Deliberately excluded

- Controller-only `internal/agentcontrol` handlers and outbox adapters.
- Controller database models, migrations, workspace/RBAC policy and service
  implementations.
- Any claim of production-ready end-to-end integration.

### Verification

- Isolated Go test, race, vet and build checks are provided by
  `scripts/verify-agent-isolated.sh` and use a compile-time NATS API stub.
- Real NATS/controller integration remains to be run in the actual deployment
  environment.
