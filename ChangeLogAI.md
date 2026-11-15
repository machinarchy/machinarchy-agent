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

## 2026-11-14 — Hybrid execution transport support

### Added

- Added opt-in `exec.command` and `exec.script` Agent capabilities backed by
  transport-neutral contracts in `machinarchy-protocol/execution/v1`.
- Added argv-based command execution, fixed-shell script execution, bounded
  stdout/stderr capture, explicit child environments, and Unix process-group
  cancellation.
- Added capability discovery to the `runtime.info` response.
- Added configuration, execution/security documentation, and unit tests for
  command/script behavior and failure output preservation.

### Changed

- The operation dispatcher now preserves normalized execution output for
  non-zero exit, timeout and cancellation outcomes.
- Updated the isolated verification harness to include the new protocol package.

### Verification

- Passed `scripts/verify-agent-isolated.sh`: unit tests, race tests, `go vet`,
  and build using the bundled NATS API stub and local protocol checkout.
- Passed protocol package tests and `go vet` using a temporary Go 1.23-compatible
  copy because the execution environment provides Go 1.23.2 while the repository
  declares Go 1.26.0.
- Real NATS/controller integration has not been performed; the isolated NATS
  stub is not an end-to-end integration test.
