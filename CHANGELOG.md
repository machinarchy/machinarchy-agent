# Changelog

User-visible changes are documented here. Internal AI-assisted implementation
notes and file-level details belong in `ChangeLogAI.md`.

## Unreleased

### Changed

- `MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY[_FILE]` accepts two keys (current
  and next, comma- or line-separated), so the controller's signing key can be
  rotated without stopping agents. The wire protocol is unchanged.

- Enrollment tokens are single-use and issued per host by a machinarchy
  owner; the shared token is for development only.
- With NATS in operator (JWT) mode, the agent connects with its own NATS user
  JWT from the controller after enrolment, limited to its own subjects,
  consumer and inbox. `MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE` then serves
  only as the bootstrap credential. Upgrade agents before enabling JWT
  issuance on the controller.
- The agent exits with code 78 when the controller rejects its enrollment or
  revokes it. The systemd unit no longer restarts it in that case
  (`RestartPreventExitStatus=78`); other failures still exit 1.

### Fixed

- Docker systemd overlay (`compose.systemd.yaml`): `service.status` and
  `service.control` failed with "System has not been booted with systemd",
  because `systemctl` in the container did not see `/run/systemd/system`. The
  overlay now mounts an empty marker directory there.

### Added

- `make systemd-verify`: `systemd-analyze verify` on the unit, in a container
  on hosts without systemd; any diagnostic fails it.
- Metrics as a Prometheus textfile for node_exporter
  (`MACHINARCHY_AGENT_METRICS_FILE`, `MACHINARCHY_AGENT_METRICS_INTERVAL`):
  connection, enrolment, lease, operations, dispatcher load, warnings and
  errors. No listener (ADR-0004).
- `--healthcheck` now needs only the state file, not the full configuration.
- `--healthcheck`: exits 0 when the agent is enrolled and its lease is live;
  the container image uses it as its health check.

### Breaking: protocol v1 and runtime rewrite (audit/agent-fix)

- The agent now builds. The non-existent `machinarchy-protocol` dependency was
  replaced by the in-repository, standard-library-only `protocol/agentv1`
  contract.
- All messages are Ed25519-signed. Agents pin the controller key
  (`MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY[_FILE]`, required) and enroll with
  proof of possession of their own key. Lease renewal no longer uses a bearer
  token.
- Subjects moved to `machinarchy-agent.v1.*`. The command stream is
  `MACHINARCHY_AGENT_COMMANDS` with consumer `agent-<id>`.
- State is one file, `MACHINARCHY_AGENT_STATE_FILE` (default
  `/var/lib/machinarchy-agent/state.json`). Previous identity, event and
  operation files are not migrated; agents must re-enroll.
- `MACHINARCHY_ENVIRONMENT` now defaults to `production` (TLS and auth
  required). New settings: NATS CA, client certificate and key, enrollment
  token file, `MACHINARCHY_AGENT_SHUTDOWN_TIMEOUT`. Removed settings now cause
  a startup error.
- `service.control` accepts only start, stop and restart locally, and calls
  `systemctl --no-ask-password -- <verb> <unit>`.
- `system.facts` output was renamed to the protocol v1 schema. The kernel
  version is read from `/proc/sys/kernel/osrelease`.
- Added `--public-key`. `--version` now shows the protocol version.

- Added Linux Docker Engine deployment for AMD64 and ARM64 with a non-root,
  read-only, capability-dropped default profile and persistent Agent state.
- Added host OS facts integration without a host-root mount and omitted the
  container overlay filesystem metric from host facts.
- Added an optional systemd D-Bus Compose overlay and host Polkit preparation
  script for exact allowlisted start/stop/restart actions; socket access checks
  support both world-accessible sockets and narrowly scoped non-root groups.
- Added Docker host integration security documentation and static checks.

- Added versioned Linux AMD64/ARM64 release archives containing the Agent
  binary, systemd unit, environment template and installation lifecycle tools.
- Added atomic native installation and upgrade, one-step binary/unit rollback,
  and non-destructive uninstall with an explicit destructive purge option.
- Fixed system account allocation to avoid hard-coded UID/GID collisions and
  corrected configuration-directory group traversal for the unprivileged Agent.
- Added release archive/checksum smoke tests and CI deployment packaging checks.
- Added `machinarchy-agent --check-config` for validating the production
  environment without connecting to NATS or starting the runtime.

- Added opt-in Linux/systemd `service.status` and `service.control` Agent
  capabilities with exact local unit allowlisting and separate control opt-in.
- Added bounded systemd status parsing, fixed-argv lifecycle actions, context
  cancellation, and a minimal child environment that excludes Agent secrets.
- Added shared `services/v1` contracts (protocol module v0.2.3).

- (Superseded) Moved the `system.facts` DTOs into the shared `machinarchy-protocol/hostfacts/v1`
  module and validate collected facts against that contract before publishing.

- Added the read-only `system.facts` capability for host inventory refresh.
  It reports a versioned, bounded snapshot of hostname, OS/kernel, architecture,
  logical CPU count and best-effort Linux memory, uptime and root-filesystem
  metrics. Sensitive details such as IP addresses, usernames, environment
  variables, process lists and machine IDs are intentionally excluded.

- Replaced inbound Core NATS event subscription with a controller-provisioned
  JetStream durable pull consumer using explicit acknowledgement and delayed
  retry for transient local failures.
- Added bounded JetStream stream/consumer binding configuration and Agent-side
  validation that the pre-provisioned consumer is scoped to the assigned Agent.
- Made queue-full terminal rejections durable in the local result outbox so
  the controller can acknowledge every terminal result consistently.
- Added controller integration overlay for JetStream stream/consumer
  provisioning, stable-ID command outbox publication and persist-before-ack
  result processing.

- Added a bounded durable Agent operation store. Accepted work is persisted
  before enqueue, `running` is persisted before execution, and interrupted
  running operations recover as `unknown` rather than being replayed.
- Added a durable result outbox with retry-until-controller-acknowledgement
  behavior and bounded backpressure when results remain unacknowledged.
- Added the Agent protocol v2 result acknowledgement event and `unknown`
  operation status. Controller support is required before deployment.

- Removed Agent-side generic command and shell-script execution from the
  minimal OS-management profile. The shared execution contract remains available
  for the controller's independent SSH transport.

- Extracted the agent runtime into an independent Go module.
- Added standalone build, test, container and systemd deployment scaffolding.
- (Superseded) Moved versioned wire contracts to a separate `machinarchy-protocol` module that was never published.
