# Machinarchy Agent

A small, unprivileged host agent for the Machinarchy control plane. It
connects outbound to NATS, enrolls with the controller, and runs a short list
of explicitly enabled, bounded OS capabilities. There is no remote shell.

| Capability | Purpose | Enabled by |
|---|---|---|
| `runtime.info` | Agent version, platform, enabled capabilities | always |
| `system.facts` | Hostname, OS, kernel, CPU, memory, uptime, root FS | always |
| `service.status` | systemd unit state for allowlisted units | `MACHINARCHY_AGENT_SERVICE_MANAGEMENT_ENABLED` |
| `service.control` | start, stop or restart allowlisted units | additionally `MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED` |

## How it works

```
controller ──signed commands (JetStream)──▶ agent ──▶ capabilities ──▶ host
     ▲                                        │
     └────────── signed results (NATS) ◀──────┘
```

- **Enrollment and identity.**
  - On first start the agent creates an Ed25519 key.
  - It enrolls with a one-time token, then renews a lease on the controller's
    cadence.
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
[docs/protocol.md](docs/protocol.md)). `internal/fakecontroller` is a working
reference implementation of the controller side.

## Quick start (development)

```bash
docker compose -f docker-compose.yml up -d          # local NATS with JetStream
cp .env.example .env                                # set MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY
set -a; . ./.env; set +a
go run ./cmd/machinarchy-agent --check-config
go run ./cmd/machinarchy-agent
```

The agent needs a controller that implements the protocol. Until machinarchy
does, the integration tests show the full flow end to end:

```bash
go test -race ./internal/app/...
```

## Build and test

```bash
make check        # fmt, tidy, vet (incl. linux/amd64+arm64), race tests, lint, build, deployment checks
make test-race    # unit + integration tests against an embedded nats-server
make docker-build
```

Requires Go 1.27 (pinned toolchain `go1.27.4`).

## Deploy

- systemd: [docs/deployment/systemd.md](docs/deployment/systemd.md)
- Docker: [docs/deployment/docker.md](docs/deployment/docker.md)
- Configuration reference: [docs/configuration.md](docs/configuration.md)
- Security model: [docs/security.md](docs/security.md)

## Layout

| Path | Responsibility |
|---|---|
| `cmd/machinarchy-agent` | Composition root and flags |
| `protocol/agentv1` | Public wire contract (standard library only) |
| `internal/config` | Environment configuration and validation |
| `internal/state` | Single atomic state file (key, identity, ledger, operations) |
| `internal/app` | NATS connection, enrollment, lease, command consumer |
| `internal/dispatch` | Bounded execution, recovery, result outbox |
| `internal/capabilities` | Host capabilities |
| `internal/fakecontroller` | Reference controller used by integration tests |

## License

[MIT](LICENSE)
