# Machinarchy Agent

**A lightweight Linux OS management runtime for Machinarchy.**

The Agent runs on managed machines and maintains an authenticated outbound
connection to the Machinarchy control plane through the configured event
broker. It receives controller-authorized work and executes only explicitly
registered capabilities.

> **Development status:** this is an early Linux OS-management implementation.
> The Agent runtime, enrollment/lease lifecycle, bounded dispatcher, durable
> local operation store/result outbox, JetStream command consumer, host facts,
> and allowlisted systemd operations are implemented. Real Go 1.26, NATS/
> JetStream, host systemd/Polkit and end-to-end controller tests are still
> required before production deployment.

## Repository responsibilities

| This repository owns | The `machinarchy` controller owns |
|---|---|
| Agent process and lifecycle | Agent enrollment authority |
| Agent-side credential storage | Agent identity and lease records |
| Outbound broker connection | Controller-side broker integration |
| Lease renewal client | Lease issuance, validation and revocation |
| Agent event subscription | Operation scheduling and authorization |
| Bounded operation dispatcher | Operation state and history |
| Explicit local capabilities | Workspace and resource authorization |
| Agent operation result publishing | Result ingestion and persistence |
| Agent packaging and upgrades | Fleet rollout policy |

Shared wire contracts are maintained in the separate
[`machinarchy-protocol`](https://github.com/machinarchy/machinarchy-protocol)
Go module. The controller and agent must use a compatible protocol release.
The current reliability work uses protocol v2; it is not compatible with a
protocol-v1 controller. The controller must persist operation results before
sending the result acknowledgement and must understand the `unknown` status.
See [`docs/operations/phase2-rollout.md`](docs/operations/phase2-rollout.md)
for the required controller changes and release order.

The product direction and capability roadmap are documented in
[`docs/architecture/operational-runtime.md`](docs/architecture/operational-runtime.md).
The reliability work is incremental: the Agent now binds to a controller-
provisioned JetStream durable pull consumer and retains terminal results until
the controller acknowledges durable persistence. The controller must provision
that consumer, publish commands from its database outbox, and durably process
results before acknowledging them. See
[`docs/operations/phase2-rollout.md`](docs/operations/phase2-rollout.md).

## Architecture at a glance

```text
Machinarchy Controller
  ├── authorizes agent identity and lease
  ├── schedules and publishes operations
  ├── validates and persists operation results
  └── owns the transactional outbox
               │
               │ versioned protocol over authenticated NATS
               ▼
Machinarchy Agent
  ├── establishes identity/session
  ├── renews lease and reports liveness
  ├── validates target and event envelope
  ├── deduplicates event identifiers locally
  ├── dispatches to explicitly registered capabilities
  └── publishes bounded operation results
```

## Current implementation

- NATS Core request/reply for registration and lease renewal, plus a JetStream
  durable pull consumer for inbound command/event delivery.
- Versioned registration and lease request/reply client.
- Controller-assigned agent identity with restrictive local file permissions.
- Heartbeat/lease renewal and session recovery logic.
- Agent-specific JetStream durable consumer binding and event envelope validation.
- Explicit message acknowledgement after durable local event handling; delayed
  negative acknowledgement on transient failures and terminal rejection of
  invalid messages.
- Bounded event buffers and a bounded event deduplication ledger.
- Bounded operation queue and worker pool.
- Per-operation deadline and cancellation support.
- Bounded, permission-protected local operation store; accepted work is persisted before queueing and the `running` transition is persisted before capability execution.
- Durable result outbox that retries terminal results until the controller acknowledges durable persistence.
- Restart recovery resumes only operations known not to have started; interrupted in-flight operations become `unknown` and are not blindly re-executed.
- Graceful shutdown/drain behavior.
- A read-only `runtime.info` capability that reports the capabilities enabled
  in the local Agent configuration.
- A read-only `system.facts` capability for controller-driven inventory
  refresh. It reports hostname, OS/kernel, architecture, logical CPU count and
  best-effort Linux memory, uptime and root-filesystem metrics. It intentionally
  excludes IP addresses, usernames, environment variables, process lists and
  machine IDs.
- Opt-in Linux/systemd `service.status` and `service.control` capabilities. They
  are restricted to an exact local `.service` allowlist; control operations
  (`start`, `stop`, `restart`) require a separate opt-in.
- Bounded operation results, context cancellation and non-zero service-manager
  result reporting. Systemd subprocesses use a minimal fixed environment and
  never inherit Agent broker credentials.

The Agent does **not** expose generic remote command or shell-script execution.
Service status and control have separate opt-ins, and both are limited to an
exact local systemd unit allowlist. All operations use the operating-system
permissions of the Agent process; the Agent never silently elevates privileges.
See [`docs/operations/phase5-service-management.md`](docs/operations/phase5-service-management.md).
The approved minimal capability scope is recorded in
[`docs/operations/phase1-minimal-agent-core.md`](docs/operations/phase1-minimal-agent-core.md).

## Agent scope and transport boundary

The Agent is an optional management runtime for hosts configured to use the
Agent transport. The controller may independently use SSH for other hosts and
workloads. The Agent itself does not connect to hosts over SSH and does not
provide generic remote command or shell-script execution. Its initial supported
capabilities are limited to `runtime.info`, `system.facts`, and explicitly
configured Linux/systemd `service.status` and `service.control`.

The shared protocol repository may contain transport-neutral execution
contracts for the controller's SSH implementation. Their presence in the
shared protocol does not enable or expose those operations through this Agent.

## Requirements

- Go 1.26 or newer.
- A Machinarchy controller implementing the matching protocol version.
- A NATS Server 2.9.0 or newer with JetStream enabled, reachable outbound from the managed host.
- Linux is the initial production target; other operating systems remain
  unsupported for production until their persistence and process-lifecycle
  behavior are verified.
- For production: `tls://` NATS endpoints and an approved NATS authentication
  method (credentials file, token, or username/password).

## Quick start

1. Create/update the `machinarchy-protocol` repository from the companion
   protocol bundle and publish protocol module `v0.2.3` after reviewing the
   protocol v2 compatibility requirements and additive `hostfacts/v1` and `services/v1` contracts.
2. Clone both repositories into a sibling workspace:

   ```text
   projects/
   ├── machinarchy-agent/
   └── machinarchy-protocol/
   ```

3. From the parent directory, create a local Go workspace:

   ```bash
   go work init ./machinarchy-agent ./machinarchy-protocol
   ```

4. Configure the agent:

   ```bash
   cp machinarchy-agent/.env.example machinarchy-agent/.env
   # Edit the file and set the broker URL and one-time enrollment token.
   chmod 600 machinarchy-agent/.env
   ```

5. Run checks and build:

   ```bash
   cd machinarchy-agent
   go mod tidy
   go mod download
   make check
   make build
   ```

6. Run the agent after the controller-side registration, lease and event
   handlers have been deployed:

   ```bash
   set -a
   . ./.env
   set +a
   ./bin/machinarchy-agent
   ```

A local NATS server alone is not sufficient: the controller must answer the
registration and lease request/reply subjects, enable JetStream, provision the
shared command stream, and create this Agent's durable pull consumer before the
session can become active. Do not deploy this protocol-v2 Agent with a v1
controller. A production rollout also requires a durable controller-side
result store and result acknowledgement after commit.

## Configuration

See [`docs/configuration/reference.md`](docs/configuration/reference.md) and
`.env.example`. Configuration values are never logged wholesale. Production
requires TLS and broker authentication.

## Development

```bash
make fmt
make test
make test-race
make vet
make build
make check
```

The isolated verification script compiles the agent against a minimal NATS API
stub and a local sibling checkout of the protocol module. It checks local Go
behavior and interfaces only; it is not a real NATS integration test. Use a real
broker and controller for end-to-end validation. Commit the generated `go.sum`
after `go mod tidy` before publishing a release.

## Deployment

The supported host-management deployment is currently the native Linux binary
under systemd on AMD64 and ARM64. A versioned GitHub release archive includes
the executable, unit file, environment template, installer, rollback helper and
uninstaller.

For an example release tag and architecture:

```bash
VERSION=v0.1.0
ARCH=amd64 # or arm64
ASSET="machinarchy-agent_${VERSION}_linux_${ARCH}.tar.gz"
BASE="https://github.com/machinarchy/machinarchy-agent/releases/download/${VERSION}"

curl --fail --location --proto '=https' --tlsv1.2 --output "${ASSET}" "${BASE}/${ASSET}"
curl --fail --location --proto '=https' --tlsv1.2 --output checksums.txt "${BASE}/checksums.txt"
grep -F "  ${ASSET}" checksums.txt | sha256sum --check
tar -xzf "${ASSET}"
cd "machinarchy-agent_${VERSION}_linux_${ARCH}"
sudo ./install-systemd.sh
```

Configure `/etc/machinarchy-agent/agent.env`, then validate it with
`machinarchy-agent --check-config` through `systemd-run` before enabling the
service. See
[Native Linux binary deployment](docs/deployment/systemd.md) for upgrades,
rollback, uninstall and security details.

Docker Engine deployment is supported for host-facts collection. Optional
systemd service management uses a separate Compose override plus an explicit
host-side Polkit policy; see [Docker Engine deployment](docs/deployment/docker.md)
and the [Phase 3 implementation record](docs/operations/phase3-docker-deployment.md).
The Agent runs unprivileged and does not require Kubernetes, privileged mode,
the Docker socket, host PID, host networking, or a host-root filesystem mount.

- [Native Linux binary / systemd](docs/deployment/systemd.md)
- [Phase 2 implementation and verification record](docs/operations/phase2-native-binary-deployment.md)
- [Docker Engine deployment](docs/deployment/docker.md)
- [Phase 3 implementation and verification record](docs/operations/phase3-docker-deployment.md)
- [Operations and troubleshooting](docs/operations/troubleshooting.md)

## Security

Review [`SECURITY.md`](SECURITY.md),
[`docs/security/threat-model.md`](docs/security/threat-model.md), and
[`docs/operations/phase5-service-management.md`](docs/operations/phase5-service-management.md)
before enabling service control on production hosts.

## Compatibility and limitations

- This release uses wire protocol version `2` from the `machinarchy-protocol`
  module. It is not compatible with a protocol-v1 controller. See
  [`docs/protocol/compatibility.md`](docs/protocol/compatibility.md).
- Inbound command delivery uses a controller-provisioned JetStream durable
  pull consumer with explicit acknowledgements. Registration/lease request-
  reply and Agent result publication still use Core NATS. The controller must
  provision the durable stream/consumer and retain its DB outbox item until a
  JetStream publish acknowledgement is received.
- Accepted operations and terminal results are persisted locally in a bounded,
  permission-protected single-process store. Results remain in the outbox until
  the controller acknowledges durable persistence. This does not protect
  against host-disk loss or provide distributed exactly-once execution.
- An operation found in `running` state after a process restart is reported as
  `unknown`; it is never blindly re-executed because its external side effects
  may already have occurred.
- Production rollout is blocked until the controller provisions per-Agent
  JetStream consumers, publishes from its durable database outbox, performs
  idempotent result persistence/acknowledgement, and passes the scenarios in
  [`docs/operations/phase2-rollout.md`](docs/operations/phase2-rollout.md)
  against real NATS, controller storage and production-equivalent ACLs.

## License

MIT. See [LICENSE](LICENSE). If the main Machinarchy repository uses a
 different license, align this repository before publishing it publicly.
