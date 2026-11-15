# Machinarchy Agent

**The distributed execution runtime for Machinarchy.**

The Agent runs on managed machines and maintains an authenticated outbound
connection to the Machinarchy control plane through the configured event
broker. It receives controller-authorized work and executes only explicitly
registered capabilities.

> **Development status:** this is an early hybrid-execution implementation.
> The Agent runtime, enrollment/lease lifecycle, event validation, bounded
> dispatcher, and opt-in local command/script capabilities are present. The
> controller-side shared execution model, capability inventory, real NATS
> integration and end-to-end transport tests must still be completed before
> production deployment. Core NATS is not a durable work queue.

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
Go module. The controller and agent must use the same protocol release.

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

- NATS Core connectivity with authentication options and reconnect callbacks.
- Versioned registration and lease request/reply client.
- Controller-assigned agent identity with restrictive local file permissions.
- Heartbeat/lease renewal and session recovery logic.
- Agent-specific event subscription and envelope validation.
- Bounded event buffers and a bounded event deduplication ledger.
- Bounded operation queue and worker pool.
- Per-operation deadline and cancellation support.
- Graceful shutdown/drain behavior.
- A read-only `runtime.info` capability that reports the capabilities enabled
  in the local Agent configuration.
- Opt-in `exec.command` and `exec.script` capabilities using shared, transport-
  neutral request/result contracts from `machinarchy-protocol/execution/v1`.
- Bounded stdout/stderr capture, explicit child-process environment, operation
  cancellation, and non-zero exit status reporting.

Command and script execution remain **disabled by default**. When enabled, work
runs with the operating-system permissions of the Agent process. The Agent
never silently elevates privileges.

## Hybrid execution role

The Agent is one execution transport in Machinarchy, alongside the controller's
SSH executor. Both transports use the same `execution/v1` contracts for command
and script inputs and normalized results. Inventory, host targeting, transport
selection, authorization, job scheduling and job history remain controller
responsibilities. The Agent does not connect to hosts over SSH.

To opt a host into Agent command and shell-script execution, explicitly set:

```dotenv
MACHINARCHY_AGENT_EXECUTION_ENABLED=true
MACHINARCHY_AGENT_EXECUTION_SHELL=/bin/sh
```

`exec.command` runs an executable plus an argument vector without shell parsing.
`exec.script` runs script text through the locally configured shell. See
[`docs/architecture/execution.md`](docs/architecture/execution.md) for the
execution contract, security boundary and current controller integration work.

## Requirements

- Go 1.26 or newer.
- A Machinarchy controller implementing the matching protocol version.
- A NATS endpoint reachable outbound from the managed host.
- For production: `tls://` NATS endpoints and an approved NATS authentication
  method (credentials file, token, or username/password).

## Quick start

1. Create the `machinarchy-protocol` repository from the companion protocol
   bundle and publish its first development tag, `v0.1.0`.
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
registration and lease request/reply subjects before the session can become
active.

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

- [Docker](docs/deployment/docker.md)
- [systemd](docs/deployment/systemd.md)
- [Operations and troubleshooting](docs/operations/troubleshooting.md)

## Security

Review [`SECURITY.md`](SECURITY.md),
[`docs/security/threat-model.md`](docs/security/threat-model.md), and
[`docs/security/execution.md`](docs/security/execution.md) before enabling
operation execution on production hosts.

## Compatibility and limitations

- Protocol version is `v1`; breaking wire changes require a new protocol major
  version or an explicitly negotiated compatibility process.
- The current transport uses NATS Core. Messages published while an agent is
  disconnected may be lost; Core NATS has no durable replay/consumer ack.
- Event/operation deduplication is local state, not a distributed exactly-once
  guarantee.
- Accepted work and unpublished results can be lost on process crash because
  a durable operation journal/result outbox is not yet implemented in this
  agent runtime.
- Controller integration must be tested with the actual controller database,
  broker adapter, ACLs and operation resource model.

## License

MIT. See [LICENSE](LICENSE). If the main Machinarchy repository uses a
 different license, align this repository before publishing it publicly.
