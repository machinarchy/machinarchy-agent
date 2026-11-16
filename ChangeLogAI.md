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


## 2026-11-14 — Durable Agent operation lifecycle

### Added

- Added `internal/agent/operation_store.go`, a bounded private atomic snapshot
  store for accepted operations, lifecycle transitions and pending results.
- Persisted operation acceptance before queueing and the running transition
  before invoking a capability.
- Added crash recovery: accepted/unexpired work may resume; interrupted running
  work becomes `unknown` and is never automatically replayed.
- Added result retry until controller acknowledgement, compact deduplication
  tombstones, storage bounds and backpressure.
- Added protocol v2 `operation.result.acknowledged` and `unknown` status
  contracts.

### Changed

- `AgentSession` now opens the durable operation store at the configured path
  or defaults to `<identity-file>.operations.json`.
- Updated lifecycle, execution, protocol compatibility and configuration
  documentation.

### Verification

- Passed `scripts/verify-agent-isolated.sh`: unit tests, race tests, `go vet`,
  and build using the bundled NATS API stub and local protocol checkout.
- Added persistence/reload, result acknowledgement, duplicate operation ID,
  accepted-operation recovery and unknown-after-crash tests.
- Real NATS, JetStream and controller integration remain unverified. The
  current Core NATS inbound subscription does not provide durable delivery.

## 2026-11-14 — Durable Agent operation state (Phase 1)

- Added a bounded, permission-protected local operation store with atomic snapshot replacement and restart recovery.
- Persisted operation acceptance before execution and the `running` transition before invoking a capability.
- Added a durable result outbox with retry-until-controller-acknowledgement semantics; acknowledged records retain compact request digests for deduplication.
- Added protocol v2 result acknowledgements and the `unknown` terminal status for operations interrupted after execution may have started.
- Added recovery, permission, deduplication and acknowledgement tests; updated configuration, security, lifecycle and compatibility documentation.
- Added an operational rollout gate describing the controller changes, release order and required end-to-end failure scenarios.
- Fixed bounded-capacity accounting after acknowledged `unknown` results, prevented late cancellation events from overwriting terminal results, and made shutdown wait for the result-outbox goroutine to exit. Added regression coverage for these paths.
- Reduced sensitive data at rest by removing raw request/event payloads from the durable snapshot at the `running` transition while retaining a digest for duplicate detection; terminal result output remains until controller acknowledgement.
- Verification: isolated Go 1.23-compatible harness with local NATS API stub passed unit tests, race detector, `go vet` and build. The repository declares Go 1.26; real NATS and controller end-to-end tests remain outstanding.
- Rollout constraint: do not pair this Agent build with a protocol-v1 controller. The controller must durably persist results before acknowledging them and must support the protocol-v2 `unknown` status and result-ack event.


## 2026-11-14 — Durable Agent command delivery (Phase 2 implementation slice)

### Added

- Replaced Agent inbound Core NATS event subscription with binding to a
  controller-provisioned JetStream durable pull consumer.
- Added explicit JetStream message acknowledgement after local durable event
  handling, delayed NAK for transient errors, and terminal handling for
  invalid-sized poison messages.
- Added controller overlay code for bounded WorkQueue stream/per-Agent consumer
  provisioning, stable-ID event publishing, a transactional-outbox adapter
  contract, and persist-before-ack result processing.
- Added tests for consumer scoping, acknowledgement behavior, stream policies,
  controller outbox publication, result persistence ordering, and identity
  validation.

### Changed

- Queue-full operation rejections are now committed to the Agent operation
  store/result outbox before the inbound JetStream event is acknowledged.
- Updated Agent transport, configuration, security, protocol and rollout docs
  to distinguish Core NATS request/reply/results from JetStream command delivery.

### Verification

- Passed `scripts/verify-agent-isolated.sh`: unit tests, race detector,
  `go vet`, and build using the bundled compile-time NATS API stub.
- Passed `controller-overlay/scripts/verify-isolated.sh`: unit tests, race
  detector, `go vet`, and build using the bundled compile-time NATS API stub.
- Live NATS/JetStream integration and controller database integration have not
  been run. The actual Machinarchy controller source checkout was not available;
  the controller package is a drop-in integration overlay, not merged into the
  controller repository.


## 2026-11-14 — Host facts capability (Phase 3 initial slice)

### Added

- Added a read-only `system.facts` capability with a versioned and bounded JSON
  response containing selected host identity, OS, kernel, architecture, CPU,
  memory, uptime, root filesystem and Agent runtime facts.
- Added Linux best-effort collection using `/etc/os-release`, `/proc/meminfo`,
  `/proc/uptime` and `statfs`; non-Linux builds return portable baseline facts
  and omit platform metrics until dedicated collectors exist.
- Added tests for schema version, cancellation, strict input validation, bounded
  JSON output and omission of sensitive fields.

### Changed

- Registered `system.facts` as a default read-only capability and included it
  in `runtime.info` capability discovery.
- Updated architecture and user-facing documentation.

### Verification

- Passed the isolated Agent test, race, vet, Linux build and Windows cross-compile
  harness with the bundled compile-time NATS stub and local protocol checkout.
  This is not a real NATS integration test and does not replace the declared
  Go 1.26 CI run.


## 2026-11-14 — Host facts shared contract and controller integration overlay (Phase 4 slice)

### Added

- Added `machinarchy-protocol/hostfacts/v1` with strict JSON decoding, schema versioning, size limits and field consistency validation.
- Added controller host facts result processing, authoritative operation matching, idempotent snapshot-store contract and authenticated HTTP route adapters.
- Added a result-processing hook that runs after generic result persistence and before Agent acknowledgement; a host-facts persistence failure therefore causes a safe retry.

### Changed

- Updated the quick-start, rollout and compatibility documentation to use protocol module v0.2.2.
- Agent facts DTOs now alias the shared protocol types and collected facts are validated before serialization.
- Bumped the Agent protocol-module dependency to v0.2.2. The Agent event envelope remains protocol v2.

### Verification

- Agent isolated harness passed shared facts contract tests, unit tests, race detector, `go vet`, Linux build and Windows cross-compilation using the compile-time NATS stub.
- Controller overlay isolated harness passed shared facts contract tests, unit tests, race detector, `go vet` and build using the compile-time NATS stub.
- No live JetStream/controller database integration was run. The backend archive available in the workspace is an older HTTP-polling controller and does not match the current NATS-based Agent/controller architecture; this remains a controller integration overlay.


## 2026-11-14 — Phase 5 bounded service management

### Added

- Added shared `services/v1` request/response contracts for service status and
  constrained lifecycle operations.
- Added opt-in Linux/systemd `service.status` and `service.control` capabilities.
- Added exact local `.service` allowlisting, a separate mutation opt-in, fixed
  systemctl argument vectors, bounded status parsing and operation cancellation.
- Added a minimal systemctl child environment so Agent broker credentials and
  arbitrary process variables are not inherited.
- Added configuration validation, documentation, rollout notes and tests for
  allowlist enforcement, supported actions, output validation and cancellation.

### Deliberately excluded

- Windows SCM and macOS launchd providers.
- `enable`, `disable`, `reload`, unit-file changes and generic service-manager
  commands.
- Desired-state reconciliation (`service.ensure`), health checks and restart
  policy management.
- Any Agent-side privilege escalation or broad privileged helper.

### Verification

- Isolated shared protocol and Agent unit tests, race detector, `go vet`, Linux
  build and Windows cross-compilation passed with the bundled compile-time NATS
  stub and Go 1.23 compatibility harness.
- Real Go 1.26 CI, real NATS/JetStream, systemd/polkit permission behavior and
  controller authorization/integration remain to be verified.


## 2026-11-14 — Phase 1 minimal Agent core and scope hardening

### Removed

- Removed Agent-side `exec.command` and `exec.script` implementations, tests,
  process-group cancellation helpers and execution-specific configuration.
- Removed obsolete execution environment settings and documentation from the
  Agent configuration template and capability reference.

### Changed

- Narrowed the Agent capability profile to `runtime.info`, `system.facts`, and
  opt-in `service.status` / `service.control` for exact local systemd unit
  allowlists.
- Added regression tests for the baseline capability set, opt-in service
  capability registration, and `runtime.info` capability advertisement.
- Extracted the generic bounded output buffer and normalized capability error
  handling from the removed execution implementation so systemd operations
  retain bounded output capture and structured failure results.
- Clarified that shared `execution/v1` contracts remain in the protocol module
  for the controller's independent SSH transport; the Agent no longer exposes
  generic remote command or shell-script execution.
- Updated the Agent README, architecture, security, configuration and phase
  documentation to match the approved minimal OS-management scope.

### Files changed

- Runtime/configuration: `internal/agent/config.go`,
  `internal/agent/config_test.go`, `internal/agent/dispatcher.go`,
  `internal/agent/dispatcher_test.go`, `internal/agent/capabilities_test.go`,
  and new `internal/agent/capability_output.go`.
- Removed: `internal/agent/execution.go`,
  `internal/agent/execution_test.go`, `internal/agent/process_unix.go`,
  `internal/agent/process_windows.go`, `docs/architecture/execution.md`, and
  `docs/security/execution.md`.
- Configuration and policy: `.env.example`, `AGENTS.md`, `SECURITY.md`,
  `docs/configuration/reference.md`, and `docs/security/threat-model.md`.
- Product and operations documentation: `README.md`, `CHANGELOG.md`,
  `docs/architecture/overview.md`, `docs/architecture/operational-runtime.md`,
  `docs/operations/phase1-minimal-agent-core.md`,
  `docs/operations/phase3-host-facts.md`,
  `docs/operations/phase5-service-management.md`,
  `docs/deployment/docker.md`, and `docs/protocol/compatibility.md`.
- Updated `scripts/verify-agent-isolated.sh` output label.

### Verification

- Passed `scripts/verify-agent-isolated.sh` with the local shared protocol
  checkout: protocol tests and vet, Agent unit tests, race detector, `go vet`,
  Linux build and Windows cross-compilation.
- `make fmt-check` passed.
- The repository declares Go 1.26.0; this environment provides Go 1.23.2 and
  cannot download the required toolchain. Verification therefore used the
  temporary Go 1.23 harness and compile-time NATS stub. Real Go 1.26 CI, actual
  NATS v1.55.0, live JetStream, systemd/Polkit and controller end-to-end tests
  remain outstanding.
