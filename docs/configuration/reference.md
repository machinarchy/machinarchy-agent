# Configuration Reference

All settings are environment variables. Configuration errors do not print
secret values. See `.env.example` for a commented template.

| Variable | Default | Description |
|---|---|---|
| `MACHINARCHY_ENVIRONMENT` | `development` | `development`, `test` or `production` |
| `MACHINARCHY_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `MACHINARCHY_AGENT_NAME` | `machinarchy-agent` | NATS client name |
| `MACHINARCHY_AGENT_VERSION` | `dev` | Agent build/version string |
| `MACHINARCHY_AGENT_NATS_URL` | required | Comma-separated NATS endpoints; preferred setting |
| `MACHINARCHY_EVENT_BROKER_URL` | fallback | Legacy broker URL fallback |
| `MACHINARCHY_NATS_URL` | fallback | Legacy NATS URL fallback |
| `MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE` | empty | NATS `.creds` file path |
| `MACHINARCHY_AGENT_NATS_TOKEN` | empty | NATS token; mutually exclusive with other auth methods |
| `MACHINARCHY_AGENT_NATS_USERNAME` | empty | NATS username; set with password |
| `MACHINARCHY_AGENT_NATS_PASSWORD` | empty | NATS password; set with username |
| `MACHINARCHY_AGENT_ENROLLMENT_TOKEN` | empty | One-time initial enrollment credential |
| `MACHINARCHY_AGENT_STATE_FILE` | `./data/agent/identity.json` (development); `/var/lib/machinarchy-agent/identity.json` (production) | Identity state path |
| `MACHINARCHY_AGENT_OPERATION_STORE_FILE` | `<identity-file>.operations.json` | Durable local operation lifecycle and result outbox |
| `MACHINARCHY_AGENT_CONNECT_TIMEOUT` | `5s` | NATS connect timeout |
| `MACHINARCHY_AGENT_STARTUP_TIMEOUT` | `30s` | Enrollment startup retry horizon |
| `MACHINARCHY_AGENT_RECONNECT_WAIT` | `2s` | NATS reconnect interval |
| `MACHINARCHY_AGENT_RECONNECT_JITTER` | `500ms` | Reconnect jitter |
| `MACHINARCHY_AGENT_DRAIN_TIMEOUT` | `10s` | Shutdown drain budget |
| `MACHINARCHY_AGENT_REQUEST_TIMEOUT` | `5s` | Control-plane request timeout |
| `MACHINARCHY_AGENT_EVENT_BUFFER_SIZE` | `64` | Local event buffer; bounded |
| `MACHINARCHY_AGENT_JETSTREAM_STREAM` | `MACHINARCHY_AGENT_EVENTS` | Controller-provisioned JetStream stream containing this Agent's event subject |
| `MACHINARCHY_AGENT_EVENT_NAK_DELAY` | `5s` | Delay before retrying a transiently failed event delivery |
| `MACHINARCHY_AGENT_OPERATION_WORKERS` | `4` | Worker count; bounded |
| `MACHINARCHY_AGENT_OPERATION_QUEUE_SIZE` | `64` | Operation queue depth; bounded |
| `MACHINARCHY_AGENT_OPERATION_TIMEOUT` | `5m` | Maximum operation timeout |
| `MACHINARCHY_AGENT_OPERATION_MAX_INPUT_BYTES` | `65536` | Maximum operation input |
| `MACHINARCHY_AGENT_OPERATION_MAX_OUTPUT_BYTES` | `262144` | Maximum operation output |
| `MACHINARCHY_AGENT_RESULT_PUBLISH_TIMEOUT` | `5s` | Result publish timeout |
| `MACHINARCHY_AGENT_EXECUTION_ENABLED` | `false` | Explicitly enables `exec.command` and `exec.script` on this host |
| `MACHINARCHY_AGENT_SERVICE_MANAGEMENT_ENABLED` | `false` | Enables read-only `service.status` on Linux/systemd; requires an exact allowlist |
| `MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED` | `false` | Separately enables `service.control` (`start`, `stop`, `restart`) |
| `MACHINARCHY_AGENT_SERVICE_ALLOWLIST` | unset | Comma-separated exact systemd `.service` unit names; required when service management is enabled |
| `MACHINARCHY_AGENT_SYSTEMCTL_PATH` | `/usr/bin/systemctl` | Absolute path to the local systemctl executable |
| `MACHINARCHY_AGENT_EXECUTION_SHELL` | `/bin/sh` | Absolute local interpreter path used by `exec.script`; controller cannot override it |
| `MACHINARCHY_AGENT_REGISTER_SUBJECT` | protocol default | Registration subject override |
| `MACHINARCHY_AGENT_LEASE_RENEW_SUBJECT` | protocol default | Lease renewal subject override |

## Execution notes

- Command execution is disabled unless `MACHINARCHY_AGENT_EXECUTION_ENABLED=true`.
- `exec.command` executes an argv vector; it does not implicitly invoke a shell.
- `exec.script` uses the locally configured shell and receives script content on stdin.
- Both execute with the Agent process OS identity and its filesystem/service sandbox.
- If command execution is enabled, the operation output limit must be at least 1024 bytes.

## Production rules

- The controller must provision the configured JetStream stream and the Agent's durable pull consumer before the Agent can enter its event-processing session. The Agent does not create broker resources.
- The Agent consumer uses explicit acknowledgements; the event is acknowledged only after durable local handling.
- Production endpoints must use `tls://`.
- Production must configure exactly one supported NATS authentication method.
- A credentials file must be regular, non-symlinked, non-empty and mode `0600`
  or more restrictive.
- Remove the one-time enrollment token from the agent environment after it
  has successfully registered.
- Protect the identity, event-ledger and operation-store files (`0600`) and their parent directory (`0700`).
- Keep the operation store on persistent local storage. Run exactly one Agent process per operation-store path; multi-process sharing is not supported. The store can contain pending operation inputs and outputs, so treat it as sensitive data.
- Never place credentials directly in a URL or commit them to `.env`.


Service control is not a privilege escalation mechanism. It runs as the Agent's
OS account and is additionally subject to systemd/polkit policy. Keep the
capabilities disabled unless the host has a reviewed local allowlist and
privilege policy. See [`../operations/phase5-service-management.md`](../operations/phase5-service-management.md).
