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
| `controller rejected request: enrollment_rejected` | Wrong token, or this key was revoked. | Use the controller's token. A revoked host is re-admitted only with a new key: stop the agent, delete the state file, start again. |
| state file errors (`permission`, `owner-only`) | The state directory is not `0700` and owned by the agent user. | Fix ownership and mode; never make it readable by others (it holds the private key). |

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
