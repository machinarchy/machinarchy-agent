# AI Change Log

## 2026-11-18 — Audit Phase 8: protocol module, fuzzing (branch audit/ops)

### Changed

- `protocol/` is now its own module, `github.com/machinarchy/machinarchy-agent/protocol`
  (`protocol/go.mod`), so machinarchy can import `agentv1` without the agent's
  internals or test dependencies. The import path of `agentv1` is unchanged;
  `go.mod` requires it through `replace ... => ./protocol`.
- `Makefile`: `test`, `test-race`, `vet`, `lint`, `vuln` and `tidy-check` run in
  both modules; new `coverage`, `gosec` and `fuzz` targets. `Dockerfile` copies
  `protocol/go.mod` before `go mod download`.
- Added `protocol/agentv1/fuzz_test.go`: `FuzzOpenCommand`,
  `FuzzOpenRegisterRequest`, `FuzzParsePublicKey`, `FuzzDecodeStrict`.
- `AGENTS.md`: the golden-fixture update command runs inside `protocol/`.

- `machinarchy-agent --healthcheck` (`cmd/machinarchy-agent/main.go`,
  `internal/state/state.go` `Health`): exits 0 only when the state file shows an
  enrolled agent with an unexpired lease. Read-only; no HTTP server is added
  (AGENTS.md). Used by the container `HEALTHCHECK`.

- `Dockerfile`: base images pinned by digest (`golang:1.27.4-bookworm`,
  `debian:bookworm-slim`), BuildKit cache mounts, `-buildvcs=false`, and a
  `HEALTHCHECK` running `--healthcheck`. `.dockerignore` is now an allowlist
  (cmd, internal, protocol; no tests, fake controller, state or credentials).
- `Makefile`: `BUILD_TIME` is the commit time (reproducible builds), `build`
  adds `-buildvcs=false`, `docker-build` uses buildx.

- `.github/workflows/ci.yml`: pinned golangci-lint v2.15.0, govulncheck
  v1.1.4 and gosec v2.22.4 (was `latest`); lint and govulncheck cover both
  modules through `make`; adds race tests, a coverage artifact, gosec and a
  20s fuzz smoke. `.github/workflows/docker.yml`: commit-time `BUILD_TIME`,
  and PR builds check the version output, the non-root user and the health
  check.

### Verification

- `make test`, `make lint`, `make tidy-check` in both modules.
- `docker buildx build` succeeds; the image reports its version, runs as
  10001:10001 with STOPSIGNAL SIGTERM and the health check;
  `scripts/test-docker-deployment.sh` passes. The first fuzz commit
  left one `revive` unused-parameter finding in `fuzz_test.go`; fixed in the
  next commit, after which `make lint` reports 0 issues in both modules.
- `internal/state/health_test.go` covers missing, unenrolled, live and expired states.
- Each fuzz target ran for 10s without findings; `make fuzz FUZZTIME=2s` passes.

## 2026-11-17 — Audit Phase 4: make the agent build, secure and testable

### Changed

- Removed every package that depended on the non-existent
  `github.com/machinarchy/machinarchy-protocol` module, the in-repo stub of
  `nats.go`, and `scripts/verify-agent-isolated.sh`.
- Added `protocol/agentv1`, a standard-library-only, Ed25519-signed and
  versioned wire contract with golden fixtures.
- Rewrote the runtime as `internal/{state,dispatch,app,config}`. Capabilities
  are decoupled from config. Added `internal/fakecontroller` as a reference
  controller.
- Hardened service control (local verb allowlist, `--`, `--no-ask-password`)
  and fixed a facts compile failure on linux/arm and ppc64le.
- Updated Dockerfile, compose files, systemd unit and env templates, Makefile,
  CI, release and docker workflows, and docs. Deleted stale phase and history
  docs.

### Verification

- `go build ./...`, `go vet ./...` (host, linux/amd64, linux/arm64),
  `go mod verify`, `go mod tidy -diff`.
- `go test -race -count=3 -shuffle=on ./...`, including integration tests
  against an embedded nats-server.
- `golangci-lint run` (strict config): 0 issues. `gosec`: 0 issues.
- `govulncheck` with go1.27.4: no reachable vulnerabilities.
- `make deployment-check` passed. systemd-analyze is unavailable on the review
  host, so that step was skipped.
- `docker build` succeeded. The image runs as 10001:10001, and `--version` /
  `--check-config` were smoke-tested.
- Not verified: a live machinarchy controller (it does not implement the
  protocol yet), real systemd/polkit, and a production NATS ACL setup.

### Follow-up fixes found by repeated test runs

- `internal/app`: commands are consumed only after the dispatcher is ready.
  Previously a restarted agent nak'ed its first command while recovering.
- `internal/app` tests: synchronize on a full round trip before restarting,
  and add a lease-expired nak/redelivery test. Logs are available via
  `AGENT_TEST_LOG=1`.
- Bumped `golang.org/x/crypto` to v0.59.0 (GO-2026-5932, not reachable).
- Final verification: `go test -race -count=5 -shuffle=on ./...` passed,
  `make check` passed, and `govulncheck` reported no vulnerabilities.

## 2026-11-16 — Split the Agent catch-all package by responsibility

### Changed

- Replaced the oversized `internal/agent` package with cohesive packages:
  `app`, `config`, `controlplane`, `transport`, `transport/nats`, `identity`,
  `events`, `operations`, and `capabilities`.
- Kept `cmd/machinarchy-agent` as the composition root; the neutral
  `internal/transport` package owns connection/lifecycle contracts, implemented
  by the NATS adapter without importing `internal/app`.
- Moved tests beside their owning package and replaced formerly shared test
  helpers with package-local fixtures where necessary.
- Exported only the small internal-module helpers required across package
  boundaries (configuration limits/path/subject validation, event bounds and
  identifiers, identity lease/request helpers, and capability registry API).
- Updated repository instructions, architecture/package maps, migration
  guidance, Docker static checks and the isolated verification harness.

### Files changed

- `cmd/machinarchy-agent/main.go`, `internal/**`, `AGENTS.md`,
  `docs/architecture/overview.md`, `docs/architecture/repository-boundary.md`,
  `docs/development/migration-from-monolith.md`,
  `internal/transport/transport.go`, `scripts/test-docker-deployment.sh`,
  `scripts/verify-agent-isolated.sh`, `ChangeLogAI.md`.

### Verification

- Passed `make fmt-check`.
- Passed `make deployment-check` (release packaging/checksum smoke tests,
  systemd unit verification, and Docker deployment static checks).
- Manually inspected the internal import graph; no package cycles or unresolved
  internal package imports were found.
- `go test ./...` is blocked before compilation: the available local toolchain
  is Go 1.23.2 while `go.mod` requires Go 1.26.0. The isolated verification
  harness is additionally blocked because the companion
  `machinarchy-protocol` checkout is not included in the supplied archive.
- `go vet` and production build were not validated for the same toolchain and
  dependency availability reasons.


## 2026-11-16 — Agent reliability and release workflow corrections

### Fixed

- Serialized the dispatcher's durable cancellation, running and completion
  transitions so cancellation cannot interleave between the in-memory
  acceptance check and the persisted running state.
- Recovered pre-execution cancellations as `cancelled` rather than `unknown`,
  while preserving `unknown` for cancellations after execution began.
- Repaired cancellation tombstones that were persisted without a completed
  result, both during startup recovery and event redelivery.
- Preserved acknowledged cancellation results when duplicate operation events
  are redelivered, preventing an acknowledged result from being recreated.
- Rejected operation-store paths that collide with the identity event ledger,
  including paths equivalent after home expansion and lexical normalization.
- Propagated identity-directory open/sync errors instead of reporting a
  successful save when the directory entry was not confirmed durable.
- Restricted Docker `latest` updates to stable SemVer tags; prerelease tags
  remain version-specific. Validated native release tags and mark SemVer
  prereleases as GitHub prereleases.
- Included both `go.mod` and `go.sum` in GitHub Actions module cache keys.
- Removed stale transport comments that referred to a controller-side broker
  package not present in this standalone Agent repository.

### Files changed

- `.github/workflows/ci.yml`, `.github/workflows/docker.yml`,
  `.github/workflows/release.yml`.
- `internal/agent/config.go`, `config_test.go`, `dispatcher.go`,
  `dispatcher_test.go`, `event_ledger.go`, `identity.go`, `nats.go`,
  `operation_store.go`, `operation_store_test.go`, `runtime.go`, `session.go`.
- `docs/architecture/lifecycle.md`, `docs/architecture/operational-runtime.md`,
  `docs/operations/compatibility-matrix.md`,
  `docs/operations/release-process.md`, `ChangeLogAI.md`.

### Documentation

- Clarified the current Agent scope in the compatibility matrix and the
  broader operational architecture decision table.
- Documented stable versus prerelease tagging in the release process and
  distinguished pre-execution cancellation recovery from uncertain started work.
- Corrected the startup lease-renewal comment to match the explicit expired-lease
  re-enrollment policy.

### Tests added

- Operation-store/event-ledger path collision validation.
- Cancellation serialization against the durable running transition.
- Recovery of pre-execution cancellation requests and incomplete cancellation
  tombstones.
- Suppression of duplicate cancellation-result publication after acknowledgement.

### Verification

- Passed `make fmt-check`.
- Passed `make deployment-check` (release archive packaging and SHA-256 checks,
  systemd unit verification, and Docker deployment static checks).
- Go tests, race detection, `go vet`, and production builds are blocked because
  the available local toolchain is Go 1.23.2 while `go.mod` requires Go 1.26.0;
  toolchain download is unavailable in this environment.
- The isolated verification harness is blocked because the companion
  `machinarchy-protocol` checkout is not included in the supplied Agent archive.
- Live NATS/JetStream/controller integration and live Docker/D-Bus/Polkit
  integration were not run.


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


## 2026-11-14 — Phase 2 native Linux binary deployment

### Added

- Added versioned Linux AMD64/ARM64 release archive packaging with the Agent
  binary, systemd unit, production environment template, install/uninstall/
  rollback scripts, install guide and build metadata.
- Added release packaging smoke tests and a `make deployment-check` target.
- Added `machinarchy-agent --check-config` and documented validation through a
  transient systemd unit using the same EnvironmentFile and Agent UID/GID.
- Added one-step rollback support that swaps current and previous binary/unit
  copies while preserving Agent identity and operation state.
- Added explicit non-destructive uninstall and separately confirmed purge mode.

### Changed

- Reworked systemd installation to use dynamically allocated system account IDs
  rather than fixed UID/GID values, verify the system manager is reachable,
  preserve existing config/state, atomically replace binaries and units, and
  restart only a service that was already active. Uninstall refuses to remove
  the binary if an active service cannot be stopped cleanly.
- Corrected `/etc/machinarchy-agent` ownership to `root:machagent` mode `0750`,
  allowing the unprivileged Agent to read its configured credentials while
  keeping configuration management restricted to root.
- Updated release CI to run tests/race/vet and deployment packaging checks, then
  publish versioned archives and checksums instead of loose architecture-named
  binaries.
- Updated native installation, release, rollback, compatibility and README
  documentation. Docker host management remains explicitly unsupported in this
  phase.

### Files changed

- `.github/workflows/release.yml`, `Makefile`, `README.md`, `CHANGELOG.md`,
  `docs/deployment/systemd.md`, `docs/operations/release-process.md`,
  `docs/operations/rollback.md`, `docs/operations/compatibility-matrix.md`,
  new `docs/operations/phase2-native-binary-deployment.md`, `ChangeLogAI.md`.
- `cmd/machinarchy-agent/main.go`, `deployments/systemd/machinarchy-agent.service`
  and new `deployments/systemd/agent.env.example`.
- `scripts/install-systemd.sh`, new `scripts/uninstall-systemd.sh`,
  `scripts/rollback-systemd.sh`, `scripts/package-release.sh`,
  `scripts/test-release-package.sh`, `scripts/verify-systemd-unit.sh`, and the
  isolated verification harness `scripts/verify-agent-isolated.sh`.

### Verification

- Shell syntax checks, release archive packaging smoke tests, SHA-256 validation
  and systemd unit verification are run via `make deployment-check`.
- Full Go 1.26 build/test/race/vet and release workflow have not been run in this
  environment because the Go 1.26 toolchain is unavailable and network access to
  the Go toolchain proxy is blocked. Real installation/upgrade/rollback against
  a systemd VM still needs to be exercised before declaring deployment
  production-ready.


## 2026-11-14 — Phase 3 Docker Engine deployment and host OS integration

### Changed
- Added the `systemctl` client to the Docker runtime image while keeping the
  Agent process unprivileged and not running systemd inside the container.
- Added the default host-facts-only Compose deployment and a separate optional
  systemd D-Bus overlay.
- Added a host preparation script that validates D-Bus socket permissions,
  avoids adding a supplemental host group when the socket is already accessible
  to other users, reserves host UID 10001 for `machagent`, and installs a
  Polkit rule restricted to start/stop/restart of explicit service units.
- Added a policy removal script.
- Added container detection so `system.facts` omits the container overlay's
  filesystem size rather than misreporting it as host root filesystem data.
- Mounts host `/etc/os-release` at `/run/machinarchy-host/os-release` rather
  than over the image's potentially symlinked `/etc/os-release`; container
  facts omit distribution metadata when the host mount is missing.
- Updated Docker image publishing to support AMD64 and ARM64.
- Updated Agent repository instructions, threat model, compatibility matrix,
  README, Docker deployment guide, and implementation history.

### Verification
- Shared protocol and Agent unit tests passed in the isolated Go 1.23 harness.
- Agent race detector, `go vet`, Linux build, CLI config validation and Windows
  test cross-compilation passed in the isolated harness.
- Native release packaging, systemd unit verification and Docker deployment
  static shell/Compose checks passed.
- Both Compose YAML files parsed successfully with PyYAML.
- Live Docker image builds, D-Bus connectivity, Polkit authorization and
  controller/NATS end-to-end tests remain unverified in this environment.
