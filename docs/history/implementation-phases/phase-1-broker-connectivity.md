# Agent Phase 1 — Broker Connectivity

## Scope

This phase gives `cmd/agent` a real process lifecycle and an outbound NATS
connection. It covers configuration, NATS authentication, initial connection,
reconnect observation, and graceful drain.

It deliberately does **not** implement agent enrollment, identity assignment,
leases, heartbeat messages, event subscriptions, or operation execution. Those
protocols must use the accepted ADRs and existing `internal/eventing` and
`internal/broker` contracts rather than guessed subjects or payloads.

## Configuration

| Variable | Required | Default | Description |
|---|---:|---|---|
| `MACHINARCHY_AGENT_NATS_URL` | Yes* | — | Comma-separated NATS endpoints |
| `MACHINARCHY_EVENT_BROKER_URL` | Yes* | — | Fallback broker URL setting |
| `MACHINARCHY_NATS_URL` | Yes* | — | Legacy fallback broker URL setting |
| `MACHINARCHY_AGENT_NAME` | No | `machinarchy-agent` | NATS client name; not agent identity |
| `MACHINARCHY_ENVIRONMENT` | No | `development` | `development`, `test`, or `production` |
| `MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE` | No | — | NATS `.creds` file (preferred) |
| `MACHINARCHY_AGENT_NATS_TOKEN` | No | — | NATS token authentication |
| `MACHINARCHY_AGENT_NATS_USERNAME` | No | — | NATS username authentication |
| `MACHINARCHY_AGENT_NATS_PASSWORD` | No | — | NATS password authentication |
| `MACHINARCHY_AGENT_CONNECT_TIMEOUT` | No | `5s` | Per-connect timeout |
| `MACHINARCHY_AGENT_STARTUP_TIMEOUT` | No | `30s` | Time allowed for the first connection |
| `MACHINARCHY_AGENT_RECONNECT_WAIT` | No | `2s` | Base NATS reconnect delay |
| `MACHINARCHY_AGENT_RECONNECT_JITTER` | No | `500ms` | Reconnect jitter |
| `MACHINARCHY_AGENT_DRAIN_TIMEOUT` | No | `10s` | Bounded connection drain timeout |

*At least one broker URL variable must be set. The first non-empty value in the
listed priority order is used. Production requires `tls://` endpoints and one
NATS authentication method. URL-embedded credentials are rejected.

A credentials file must be a regular, non-symlink file with permissions no
broader than `0600` (for example, `chmod 0600 /etc/machinarchy/agent.creds`).

## Local development

```bash
export MACHINARCHY_ENVIRONMENT=development
export MACHINARCHY_AGENT_NATS_URL=nats://127.0.0.1:4222
export MACHINARCHY_LOG_LEVEL=info

go run ./cmd/agent
```

For production, use a TLS endpoint and NATS credentials:

```bash
export MACHINARCHY_ENVIRONMENT=production
export MACHINARCHY_AGENT_NATS_URL=tls://nats.example.internal:4222
export MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE=/etc/machinarchy/agent.creds
export MACHINARCHY_LOG_LEVEL=info
```

## Delivery and architecture boundaries

This phase only establishes the NATS connection. It does not subscribe or
publish application events, and therefore does not claim any message-delivery
guarantee. Core NATS subscriptions are not durable; operation delivery and
recovery semantics must be chosen with the existing eventing design before
message consumption is added.

The current adapter uses `github.com/nats-io/nats.go` directly. Before Phase 2,
reconcile it with `internal/broker/nats.go` and `internal/broker/publisher.go`.
If those packages already expose the needed connection lifecycle, replace this
adapter with the existing abstraction rather than maintaining two competing
NATS clients.
