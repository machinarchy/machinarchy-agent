# AI Change Log

## 2026-11-25 — Release channels: alpha and beta pre-releases via Release Please (branch release/initial)

- **Same model as machinarchy.** The agent no longer releases by pushing a `v*` tag by hand. Pushes to `main` (stable), `beta` and `alpha` run CI and then `release.yml`, now a reusable workflow: Release Please computes the version from Conventional Commits (suffix `-beta.N` / `-alpha.N` on the pre-release branches; the first version of every channel is `0.0.1`), opens a release PR, and on merge creates the tag and the GitHub Release. The job then builds the `linux/amd64` and `linux/arm64` binaries at that exact commit, packages them with `scripts/package-release.sh`, uploads them with `checksums.txt`, and attests the archives (`id-token` and `attestations` write, granted by the calling job).
- **Safeguards:** the job refuses a tag whose suffix does not match its branch; `docker.yml` (unchanged) still starts on the new tag and only a stable tag moves `latest`; the CI concurrency group no longer cancels a push run, only pull request runs, so a release is not cut off.
- **Per-channel files:** `.github/release-please/config.{stable,beta,alpha}.json` and `manifest.*.json` (empty). `CHANGELOG.md` stays hand-written (`skip-changelog`); GitHub release notes come from the commits.
- **Removed from the release job:** the format, vet, race-test and deployment checks it used to repeat; `ci.yml` runs them before the release job and gates it.
- **Docs:** `.github/WORKFLOWS.md` (new) describes the workflows, the secret `MY_GITHUB_TOKEN`, the channels and the setup.
- **Checks run:** the workflow YAML and JSON parse; a cross-reference script (job needs, outputs, reusable inputs, secrets, permissions, make targets, scripts) found nothing; the tag-against-branch guard and the tag regexes were tested for stable, beta and alpha; `package-release.sh`'s version check accepts `v0.0.1-beta.1`. **Not run:** the workflows and Release Please (they need GitHub); the first run may need fixes.

## 2026-11-25 — CI before the first push: pinned action, aligned scanners (branch release/initial)

- `.github/workflows/ci.yml`: `actions/upload-artifact` is pinned by commit SHA (v4.6.2) like every other action; govulncheck is v1.9.0 (the version the audit verified with; v1.1.4 predates Go 1.27); the separate gosec step is removed because golangci-lint already runs the gosec rules (`.golangci.yml`) and the old standalone gosec could not load Go 1.27 packages. `make gosec` stays for local use.
- **Checks run:** the workflow YAML parses; every `uses:` is pinned by SHA. The workflows were not run (they have never run on GitHub).

## 2026-11-19 — Audit Phase 16: two pinned controller keys and KMS-ready sealing (branch audit/keys)

- `protocol/agentv1/envelope.go`: `SealWith(crypto.Signer, …)` signs with any Ed25519 signer (a KMS) and verifies the returned signature before building the envelope; `Seal` delegates to it. `OpenAny([]ed25519.PublicKey, …)` accepts a message any pinned key verifies; `Open` delegates to it. Standard library only; no wire change (golden fixtures unchanged).
- `internal/config`: `ControllerPublicKeys` (was `ControllerPublicKey`), one or two keys from the variable (comma) or file (lines); a third key, duplicates and bad keys are refused. `internal/app` verifies with `OpenAny`.
- Docs: `docs/configuration.md`, `docs/protocol.md` (rotation needs no wire change), `docs/runbook.md` "Controller signing-key rotation" (5 steps), env example, CHANGELOG.

### Verification

- `TestSealWithSignerAndOpenAny`: `SealWith` output equals `Seal` (Ed25519 is deterministic); a signer returning another key's signature is refused; current and next keys open, an unpinned key does not, `Open` with one key refuses the other; no keys is an error. Golden tests pass without `-update`.
- `TestControllerKeysForRotation`: two keys by variable and by file; three keys, a duplicate, a bad key refused.
- `TestAgentAcceptsEitherPinnedControllerKey`: an agent pinning `[other, controller]` serves commands; `TestResponsesFromUntrustedControllerAreIgnored` still passes.
- `make check` passes (fmt, tidy, vet ×3, race tests, lint 0 issues, build, deployment-check). Two earlier runs failed `TestAgentEndToEnd` at its 20s budget, at the `service.status` step, while the machine's load average was about 1000. After a reboot (load about 7) every run passed. The step runs no code this change touches.

## 2026-11-19 — Audit Phase 15: real-systemd findings fixed, unit verification target (branch audit/agent-ops)

- **F15-01, Docker overlay broken** (`compose.systemd.yaml`): `systemctl` refuses the system bus unless `/run/systemd/system` exists (`EHOSTDOWN`, "System has not been booted with systemd"). The overlay mounted only the D-Bus socket, so every `service.*` call failed in Docker on any host. Added an empty root-owned tmpfs (`size 4096`, mode 0755) at that path; the host's `/run/systemd` is not exposed. `docs/deployment/docker.md` explains it.
- **F15-02, metrics** (`internal/app/app.go`): a nil-error disconnect (the agent closing its bootstrap connection after enrolment) was counted in `machinarchy_agent_nats_disconnects_total`. Only disconnects with an error count now.
- **F15-03, unit verification** (`Makefile`): new `make systemd-verify`. It runs `scripts/verify-systemd-unit.sh` on the host, or in an Ubuntu 24.04 container pinned by digest when the host has no `systemd-analyze` (this Mac). `systemd-analyze verify` only warns on bad directives and exits 0, so the target fails on any `*.service:<line>` diagnostic. The script itself is unchanged.
- `docs/deployment/systemd.md` §6: a Polkit rule for `service.control`, as tested. README lists the new target. CHANGELOG.

### Verification

- Real-systemd lab (privileged Ubuntu 24.04 container, systemd 255, D-Bus, polkitd; machinarchy controller and NATS in operator mode): see machinarchy `AUDIT/15-agent-ops.md` §3 for the full log.
- Docker overlay: the image (`make docker-build`) against a second systemd container whose D-Bus socket is shared through a volume, prepared with the repository's `scripts/prepare-docker-host.sh demo.service`. Before the fix `service.status` and `service.control` returned `execution_failed`; after, status succeeded and restart changed the unit's PID (193 → 208). A non-allowlisted unit was rejected. `docker compose config` of `compose.yaml` + `compose.systemd.yaml` shows the merged tmpfs mount.
- `TestMetricsDoNotCountThePlannedBootstrapClose` fails without the fix (`disconnects_total 1`) and passes with it.
- `make systemd-verify` passes on the shipped unit and fails on a copy with `Restart=sometimes-maybe` (the script alone printed "passed" for it).

## 2026-11-19 — Audit Phase 15: metrics textfile (ADR-0004) and a health check that needs only the state file (branch audit/agent-ops)

- **ADR-0004** (`docs/architecture/decisions/ADR-0004-metrics-textfile.md`): metrics as a Prometheus textfile for node_exporter, chosen by the owner over a Unix socket or a localhost listener. No listener is added.
- **New `internal/metrics`**: `Recorder` (reconnects, disconnects, last lease renewal, warning/error counts and times), a `slog` handler wrapper that counts every warning and error, `Render` (Prometheus text, deterministic order, escaped labels), `WriteFile` (temp file + rename, mode 0644 despite the unit's `UMask=0077`), and `Writer` (writes at once, on each interval, and once more at stop; a failing path is logged once until it recovers).
- **Agent wiring** (`internal/app/app.go`): `New` takes the recorder; `Run` starts the writer when `MACHINARCHY_AGENT_METRICS_FILE` is set and stops it last, so the final file shows the agent disconnected. Connection state is read from the current `*nats.Conn` at write time; lease renewals and registrations are recorded.
- `internal/state`: `Store.Counts()` counts operations by phase and result status and unacknowledged results without cloning the document. `internal/dispatch`: `Dispatcher.Stats()` (queue depth, running).
- `internal/config`: `MACHINARCHY_AGENT_METRICS_FILE` (absolute `*.prom`), `MACHINARCHY_AGENT_METRICS_INTERVAL` (default 15s, 1s–10m), and `StateFilePath`, shared by `Load` and the health check.
- **Q-24:** `--healthcheck` resolves the state file from `MACHINARCHY_AGENT_STATE_FILE` (or the default) and runs before `config.Load`, so it no longer needs the NATS URL, controller key or secrets.
- Docs: `docs/configuration.md` (settings, metric list, suggested alerts), `docs/deployment/systemd.md` §7 (drop-in `ReadWritePaths=` and node_exporter flag; platforms renumbered §8), `docs/runbook.md`, README, `deployments/systemd/agent.env.example`, `CHANGELOG.md`.

### Verification

- `internal/metrics`: rendering (every metric, TYPE before samples, deterministic, label escaping), the handler counts warnings even when the output level is error, atomic write with mode 0644 under umask 077 and no temp files left, final write at stop, a failing path logged once, path validation.
- `TestMetricsTextfile` (agent against embedded NATS and the fake controller): enrolled 1, connected 1, one `done/succeeded` operation, queue capacity, lease timestamps set; after stop the file shows `nats_connected 0`.
- `TestHealthcheckNeedsOnlyTheStateFile`: only `MACHINARCHY_AGENT_STATE_FILE` set; no file → 1 without a configuration error, not enrolled → 1, enrolled with a live lease → 0.
- `TestMetricsSettings`, `TestStateFilePath`.
- `make check`: fmt-check, tidy-check, vet (host, linux/amd64, linux/arm64), test-race, lint (0 issues), build, deployment-check — all pass. `verify-systemd-unit.sh` skipped (no `systemd-analyze` on macOS); covered in the next change.

## 2026-11-18 — Audit Phase 12: docs for per-host tokens and operator mode (branch audit/agent-authz)

- `README.md` (how it works, quick start, configure) and `docs/configuration.md`: enrolment tokens are single-use and issued per host by a machinarchy owner (`POST /api/v1/agents/enrollment-tokens`). The quick start shows the bootstrap credential for operator mode, and points to machinarchy's `deployments/nats/generate`.

### Verification

- Documentation only; checked against machinarchy `a35d557` (token API) and `e1eeb5f` (generator). No code changed.

## 2026-11-18 — Audit Phase 12: agent connects with its own NATS credential (branch audit/agent-authz)

### Changed

- `internal/app/app.go`:
  - The agent starts with its stored NATS user JWT while it is valid, and otherwise with the configured bootstrap credential.
  - After registration or renewal returns a JWT, it reconnects as its own NATS user. The JWT is read from state on every reconnect, the server nonce is signed with the identity key, and the inbox is `_INBOX_AGENT.<id>`.
  - Renewals store fresh JWTs.
  - The planned `authentication expired` disconnect logs at info, not warn.
- `internal/state/state.go`: `Identity.NATSUserJWT`. It is not a secret without the identity key.
- `internal/fakecontroller`: optional JWT issuing (`IssueNATSCredentials`, `MintAgentJWT`), the controller inbox prefix, extra connect options, and `OperatorMode`, which sets up an embedded NATS server with operator, system and JetStream accounts for tests.
- `go.mod`: `nats-io/jwt/v2` and `nkeys` become direct requirements (they were already indirect; same versions). This adds no broker, HTTP or persistence dependency.
- `docs/configuration.md`: the credentials file is the bootstrap credential in operator mode. `docs/runbook.md`: new "NATS credentials" table. The "exit 78" paragraph, which the previous change had placed inside the startup table, is moved below it.

### Verification

- `go test -race ./internal/app/ -run TestOperatorMode`: pass.
  - `TestOperatorModeAgentUsesItsOwnCredential` (8 s JWT lifetime): the agent switches to its credential, survives the server closing the connection at expiry, and on restart with an expired JWT goes through the bootstrap credential. A traced run showed each of these log lines.
  - `TestOperatorModePermissions`: 7 denials for another agent's JWT and 5 for the bootstrap credential.
- `make check`: exit 0 (lint 0 issues in both modules).

## 2026-11-18 — Audit Phase 12: per-agent NATS credentials in the protocol (branch audit/agent-authz)

### Changed

- `protocol/agentv1/messages.go`: `RegisterResponse` and `RenewResponse` gain optional `nats_user_jwt` (shape-checked, at most 8 KiB; forbidden on error responses).
- `protocol/agentv1/subjects.go`: `AgentInboxRoot` and `AgentInboxPrefix(agentID)` (`_INBOX_AGENT.<id>`), `ControllerInboxPrefix` (`_INBOX_CONTROLLER`), `BootstrapInboxRoot`, and the NATS permission lists `AgentPermissions(agentID)` and `BootstrapPermissions()`, so controller and agent share one definition (machinarchy AUDIT/10 Phase 12, M-10).
- New golden fixtures `register_response.json` and `renew_response.json`; the existing fixtures are unchanged.
- `docs/protocol.md`: new "NATS credentials" section, with the compatibility note. `ProtocolVersion` stays 1: the field is additive and sent only when the controller issues JWTs, but agents built earlier reject responses that carry it (strict decoding), so agents must be upgraded first.

### Verification

- `cd protocol && go test ./agentv1 -run Golden -update`, then `go test ./...`: pass. New `TestNATSUserJWTShape` (8 cases) and `TestAgentPermissionsAreScopedToOneAgent`.

## 2026-11-18 — Audit follow-up: exit code 78 after rejection (branch audit/followup)

### Changed

- `internal/app/app.go`: new `ErrEnrollmentRejected` (wraps the controller's
  `enrollment_rejected` error) and `NeedsOperator(err)`, true for it and for
  `ErrReenrollmentRequired` (lease revoked or agent unknown).
- `cmd/machinarchy-agent/main.go`: exits 78 (`EX_CONFIG`) when
  `NeedsOperator` is true, 1 for every other runtime error. The log line
  carries `exit_code`.
- `deployments/systemd/machinarchy-agent.service`: `RestartPreventExitStatus=78`,
  so a revoked or rejected agent stays stopped instead of re-enrolling until
  the start limit (5 in 5 min) fails the unit.
- `compose.yaml`: comment that Docker restart policies cannot skip an exit
  code; `docs/runbook.md` documents exit 78 and what to do under compose.

Reason: AUDIT/09 F9-02. The Phase 9 live run showed a revoked agent exiting 1,
which supervisors restart.

### Verification

- `go test -race -count=1 ./cmd/... ./internal/app/`: pass. New
  `TestExitCodeStopsSupervisorRestartsOnlyWhenOperatorMustAct`;
  `TestEnrollmentRejectedStopsAgent` and `TestRevokedLeaseClearsIdentity`
  now also assert `NeedsOperator`.
- `make check`: exit 0 (lint 0 issues).
- Not verified: `systemd-analyze verify` (not installed on the review host;
  the script skips itself) and a real systemd restart.

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

- `README.md`: machinarchy's agent gateway is now the controller side; quick
  start against it; build, test, configure, deploy and operate sections
  (health check, logs, shutdown). The token is no longer called one-time.
- `docs/runbook.md`: startup failures, health, ignored commands, crashes,
  revocation.

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
