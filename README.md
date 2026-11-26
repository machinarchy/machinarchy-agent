# Machinarchy Agent

A small, unprivileged host agent for the Machinarchy control plane. It
connects outbound to the controller's agent gateway (gRPC over TLS), enrolls, and runs a short list
of explicitly enabled, bounded OS capabilities. There is no remote shell.

| Capability | Purpose | Enabled by |
|---|---|---|
| `runtime.info` | Agent version, platform, enabled capabilities | always |
| `system.facts` | Hostname, OS, kernel, CPU, memory, uptime, root FS | always |
| `service.status` | systemd unit state for allowlisted units | `MACHINARCHY_AGENT_SERVICE_MANAGEMENT_ENABLED` |
| `service.control` | start, stop or restart allowlisted units | additionally `MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED` |

## How it works

```
controller ──signed commands (gateway stream)──▶ agent ──▶ capabilities ──▶ host
     ▲                                                │
     └────────── signed results (gateway) ◀───────────┘
```

- **Enrollment and identity.**
  - On first start the agent creates an Ed25519 key.
  - It enrolls with a single-use enrollment token that an owner issues for
    this host, then renews a lease on the controller's cadence. A revoked
    agent is refused when it tries again.
  - It needs only the controller's server URL and the enrollment token: no
    broker address or credential exists on the host. The controller's agent
    gateway authenticates the agent by its signed requests and a short-lived
    lease (see [docs/protocol.md](docs/protocol.md#transport)).
  - The controller's public key is pinned in configuration. Commands and
    responses not signed by it are rejected.
- **At-most-once execution.**
  - Every command is checked for signature, addressee, expiry and replay
    before it runs.
  - Operations run at most once.
  - If the agent stops mid-operation, the operation is reported as `unknown`
    and never replayed.
- **Durable state.** Results are kept in one atomic, owner-only state file and
  republished until the controller acknowledges them.

The wire contract is [`protocol/agentv1`](protocol/agentv1) (see
[docs/protocol.md](docs/protocol.md)). It is its own standard-library-only Go
module, `github.com/machinarchy/machinarchy-agent/protocol`, which the
machinarchy controller imports. machinarchy's agent gateway is the production
controller side; `internal/fakecontroller` is the in-repository reference used
by the integration tests.

## Quick start (development)

Run machinarchy with its agent gateway enabled (see its
`docs/operations.md#agent-gateway`), then point the agent at the gateway URL:

```bash
cp .env.example .env
# MACHINARCHY_AGENT_SERVER_URL=http://127.0.0.1:8443   (https:// outside development)
# MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY=<GET /api/v1/agents/controller-key>
# MACHINARCHY_AGENT_ENROLLMENT_TOKEN_FILE=<a file holding the token from
#   POST /api/v1/agents/enrollment-tokens, mode 0600>
set -a; . ./.env; set +a
go run ./cmd/machinarchy-agent --check-config
go run ./cmd/machinarchy-agent
```

The agent then appears in `GET /api/v1/agents`, and
`POST /api/v1/agents/{id}/invoke {"capability":"runtime.info"}` runs a
capability through it.

## Build

```bash
make build          # bin/machinarchy-agent, reproducible (-trimpath, -buildvcs=false, commit-time BUILD_TIME)
make docker-build   # digest-pinned bases, non-root (10001), HEALTHCHECK
bin/machinarchy-agent --version
```

Requires Go 1.27 (pinned toolchain `go1.27.4`).

## Test

```bash
make check          # fmt, tidy, vet (host, linux/amd64, linux/arm64), race tests, lint, build, deployment checks
make systemd-verify # systemd-analyze verify on the unit; uses a container when the host has no systemd
make test-race      # both modules; integration tests run against the in-process fake controller
make coverage       # race + coverage.out
make fuzz           # protocol decoder fuzz targets for FUZZTIME (default 30s)
make vuln gosec     # govulncheck (both modules) and gosec
```

The cross-repository end-to-end test (a real agent enrolling with a real
machinarchy) lives in machinarchy's `test/e2e`; machinarchy's CI runs it.

## Configure

Environment variables only, validated at startup (`--check-config`); keys and
tokens may come from files. Required: `MACHINARCHY_AGENT_SERVER_URL`,
`MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY[_FILE]`, and an enrollment token on
first start (single-use, issued per host by a machinarchy owner). Production
(the default) requires an `https://` server URL; the certificate is always
verified.
Full reference: [docs/configuration.md](docs/configuration.md).

## Deploy and operate

- systemd: [docs/deployment/systemd.md](docs/deployment/systemd.md)
- Docker: [docs/deployment/docker.md](docs/deployment/docker.md)
- Health: `machinarchy-agent --healthcheck` exits 0 while the agent is
  enrolled and holds an unexpired lease (the image's `HEALTHCHECK`). It needs
  only the state file. The agent serves no HTTP.
- Metrics: optional Prometheus textfile for node_exporter
  (`MACHINARCHY_AGENT_METRICS_FILE`, [ADR-0004](docs/architecture/decisions/ADR-0004-metrics-textfile.md)).
- Logs: JSON on stdout; failures carry the cause under `error`.
- Shutdown: SIGTERM gives running operations `MACHINARCHY_AGENT_SHUTDOWN_TIMEOUT`
  (default 20s), then closes the connection;
  anything cancelled is reported `unknown`, never replayed.
- Failure modes: [docs/runbook.md](docs/runbook.md). Security model:
  [docs/security.md](docs/security.md).

## Layout

| Path | Responsibility |
|---|---|
| `cmd/machinarchy-agent` | Composition root and flags |
| `protocol/agentv1` | Public wire contract (standard library only) |
| `internal/config` | Environment configuration and validation |
| `internal/state` | Single atomic state file (key, identity, ledger, operations) |
| `internal/app` | Enrollment, lease, command stream consumer |
| `internal/transport` | gRPC client of the controller's agent gateway |
| `internal/dispatch` | Bounded execution, recovery, result outbox |
| `internal/capabilities` | Host capabilities |
| `internal/fakecontroller` | Reference controller used by integration tests |

## License

[MIT](LICENSE)
