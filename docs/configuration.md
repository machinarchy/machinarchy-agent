# Configuration

All settings are environment variables. Run `machinarchy-agent --check-config`
to validate a configuration without connecting. Settings from earlier builds
that no longer exist are rejected rather than ignored. This includes every
`MACHINARCHY_AGENT_NATS_*` setting: agents no longer connect to a broker, so
existing agents must be re-enrolled against the controller's server URL.

| Variable | Default | Notes |
|---|---|---|
| `MACHINARCHY_ENVIRONMENT` | `production` | `production` requires an `https://` server URL; `development` also allows `http://` |
| `MACHINARCHY_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` (JSON logs on stdout) |
| `MACHINARCHY_AGENT_SERVER_URL` | — (required) | The controller's agent gateway, `https://host[:port]` (no path or credentials). `http://` is accepted only with `MACHINARCHY_ENVIRONMENT=development`. The certificate is always verified |
| `MACHINARCHY_AGENT_SERVER_CA_FILE` | system roots | PEM file with extra trusted roots for a private controller CA |
| `MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY` or `_FILE` | — (required) | Base64 Ed25519 key that signs commands and responses. During a [signing-key rotation](runbook.md#controller-signing-key-rotation), two keys (current and next) separated by a comma, or one per line in the file |
| `MACHINARCHY_AGENT_ENROLLMENT_TOKEN` or `_FILE` | — | Single-use token issued for this host by a machinarchy owner (`POST /api/v1/agents/enrollment-tokens`). Needed only until the first registration; the file must be 0600 |
| `MACHINARCHY_AGENT_STATE_FILE` | `/var/lib/machinarchy-agent/state.json` (dev: `./data/agent/state.json`) | The directory is created 0700 and the file is 0600 |
| `MACHINARCHY_AGENT_REQUEST_TIMEOUT` | `5s` | Register, renew and result calls |
| `MACHINARCHY_AGENT_SHUTDOWN_TIMEOUT` | `20s` | Time running operations get before being cancelled (reported `unknown`) |
| `MACHINARCHY_AGENT_OPERATION_WORKERS` | `4` | 1–64, ≤ queue size |
| `MACHINARCHY_AGENT_OPERATION_QUEUE_SIZE` | `32` | 1–256; excess requests are rejected with `queue_full` |
| `MACHINARCHY_AGENT_OPERATION_TIMEOUT` | `5m` | Upper bound on any operation (≤ 1h) |
| `MACHINARCHY_AGENT_OPERATION_MAX_OUTPUT_BYTES` | `65536` | ≤ 262144; larger output gives `output_too_large` |
| `MACHINARCHY_AGENT_SERVICE_MANAGEMENT_ENABLED` | `false` | Enables `service.status` |
| `MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED` | `false` | Additionally enables `service.control` (start, stop, restart) |
| `MACHINARCHY_AGENT_SERVICE_ALLOWLIST` | — | Exact `.service` units, comma-separated (required when service management is on) |
| `MACHINARCHY_AGENT_SYSTEMCTL_PATH` | `/usr/bin/systemctl` | Absolute path |
| `MACHINARCHY_AGENT_METRICS_FILE` | — (off) | Prometheus textfile to rewrite, an absolute `*.prom` path (ADR-0004). See [metrics](#metrics). |
| `MACHINARCHY_AGENT_METRICS_INTERVAL` | `15s` | How often the metrics file is rewritten (1s–10m) |

Systemd should allow the agent to stop for at least
`SHUTDOWN_TIMEOUT + DRAIN_TIMEOUT` plus a margin. The shipped unit uses
`TimeoutStopSec=35s`, and the Compose file uses `stop_grace_period: 30s`.

## Metrics

The agent opens no listener. With `MACHINARCHY_AGENT_METRICS_FILE` set it
rewrites a Prometheus textfile atomically on each interval and once more when
it stops ([ADR-0004](architecture/decisions/ADR-0004-metrics-textfile.md)).
Point node_exporter's textfile collector at the directory.

| Metric | Meaning |
|---|---|
| `machinarchy_agent_info{version}` | Always 1 |
| `machinarchy_agent_metrics_written_timestamp_seconds` | Last write; stale means the agent is not running |
| `machinarchy_agent_enrolled` | 1 when the agent holds an identity |
| `machinarchy_agent_controller_connected` | 1 while the connection to the controller is up |
| `machinarchy_agent_controller_reconnects_total`, `_disconnects_total` | Since start |
| `machinarchy_agent_lease_expiry_timestamp_seconds` | Commands pause after this |
| `machinarchy_agent_lease_renewed_timestamp_seconds` | Last registration or renewal; lease age is `time() -` this |
| `machinarchy_agent_operations{phase,status}` | Operations in the state file (queued, running, done by result status) |
| `machinarchy_agent_results_awaiting_ack` | Results the controller has not acknowledged |
| `machinarchy_agent_dispatch_queue_depth`, `_queue_capacity`, `_running` | Dispatcher load |
| `machinarchy_agent_log_messages_total{level}` | Warnings and errors since start |
| `machinarchy_agent_last_warning_timestamp_seconds`, `_last_error_timestamp_seconds` | 0 if none since start |

Suggested alerts: written timestamp older than 3 intervals; `controller_connected`
0 for 5 minutes; lease expiry in the past; `results_awaiting_ack` growing;
`queue_depth` near `queue_capacity`.

## Flags

- `--version` prints the build and protocol version.
- `--check-config` validates the configuration and exits.
- `--public-key` prints the agent's identity key, creating the state file on first use.
