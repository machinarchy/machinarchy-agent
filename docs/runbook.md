# Agent runbook

Logs are JSON on stdout (`journalctl -u machinarchy-agent` or `docker logs`);
read the `msg` and `error` fields. Controller-side symptoms are in
machinarchy's `docs/runbook.md`.

## The agent exits at startup

| Output | Cause | Fix |
|---|---|---|
| `invalid configuration: ...` (exit 2) | A setting is missing or invalid. | `machinarchy-agent --check-config` names it. Production requires NATS authentication and TLS. |
| `unsupported settings are set (remove them)` | A setting from an older version is still set. | Remove the listed variables. |
| `agent is not registered and no enrollment token is configured` | No identity in the state file and no token. | Set `MACHINARCHY_AGENT_ENROLLMENT_TOKEN[_FILE]`. |
| `controller rejected enrollment ...` (exit 78) | Wrong token, or this key was revoked. | Use the controller's token. A revoked host is re-admitted only with a new key: stop the agent, delete the state file, start again. |
| `controller rejected this agent's identity ...` (exit 78) | An owner revoked the agent, or the controller no longer knows it; the identity is cleared. | Intended after a revocation. To re-admit the host, delete the state file and enrol it with a new token. |
| state file errors (`permission`, `owner-only`) | The state directory is not `0700` and owned by the agent user. | Fix ownership and mode; never make it readable by others (it holds the private key). |

Exit code 78 means a restart cannot help. The systemd unit does not restart
on it (`RestartPreventExitStatus=78`). Docker's restart policy cannot tell
exit codes apart, so under compose stop the service (`docker compose stop`)
until the host is re-enrolled. Every other failure exits 1 and is restarted.

## NATS credentials (operator mode)

| Log | Meaning | Action |
|---|---|---|
| `NATS credential expired; reconnecting with the renewed one` (info) | The server closed the connection when the agent's NATS user JWT expired. Expected once per JWT lifetime (1 hour by default). | None. |
| `stored NATS credential has expired; connecting with the bootstrap credential to renew it` | The agent was down longer than the JWT lifetime. | None, if the bootstrap credential is configured. Without one, the agent cannot reconnect: configure `MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE`. |
| `asynchronous NATS error` with `Permissions Violation` | The agent touched a subject outside its permissions. This means a version mismatch with the controller, or a tampered state file. | Upgrade both sides together; if it persists, revoke the agent and re-enrol it. |
| Repeated `disconnected from NATS` with `Authorization Violation` | The JWT was not accepted: wrong account, the controller's signing key changed, or the agent was revoked and its JWT has lapsed. | Check the controller's `MACHINARCHY_AGENT_GATEWAY_NATS_ACCOUNT*` settings. A revoked agent stays out by design. |

## Running but unhealthy

`machinarchy-agent --healthcheck` prints why:

- `not enrolled`: registration has not succeeded yet. Look for
  `retrying` lines with the cause (NATS down, controller not answering).
- `lease expired at ...`: renewals are failing. Check NATS connectivity
  (`disconnected from NATS; reconnecting`) and that the controller's agent
  gateway is ready (`/healthz/ready` on machinarchy).

## Commands are ignored

- `discarding unauthenticated or malformed command`: the command was not
  signed by the pinned controller key. After rotating the controller key,
  every agent needs the new `MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY`.
- `discarding command` with an expiry or replay error: clocks differ by more
  than 2 minutes, or the command was already seen. Fix NTP.
- Capability results `rejected` / `capability_unavailable`: the capability is
  not enabled on this host (`service.*` need explicit opt-in and an
  allowlist).

## After a crash

Operations that were running when the agent stopped are reported `unknown`
on restart and never run again: check the host before retrying. Results not
yet acknowledged by the controller are republished.

## Revocation

When an owner revokes the agent, its next lease renewal fails with
`lease_revoked`; the agent clears its identity and stops. It cannot enrol
again with the same key.
