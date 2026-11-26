# Agent runbook

Logs are JSON on stdout (`journalctl -u machinarchy-agent` or `docker logs`);
read the `msg` and `error` fields. Controller-side symptoms are in
machinarchy's `docs/runbook.md`.

## The agent exits at startup

| Output | Cause | Fix |
|---|---|---|
| `invalid configuration: ...` (exit 2) | A setting is missing or invalid. | `machinarchy-agent --check-config` names it. Production requires an `https://` server URL. |
| `unsupported settings are set (remove them)` | A setting from an older version is still set. | Remove the listed variables. |
| `agent is not registered and no enrollment token is configured` | No identity in the state file and no token. | Set `MACHINARCHY_AGENT_ENROLLMENT_TOKEN[_FILE]`. |
| `controller rejected enrollment ...` (exit 78) | Wrong token, or this key was revoked. | Use the controller's token. A revoked host is re-admitted only with a new key: stop the agent, delete the state file, start again. |
| `controller rejected this agent's identity ...` (exit 78) | An owner revoked the agent, or the controller no longer knows it; the identity is cleared. | Intended after a revocation. To re-admit the host, delete the state file and enrol it with a new token. |
| state file errors (`permission`, `owner-only`) | The state directory is not `0700` and owned by the agent user. | Fix ownership and mode; never make it readable by others (it holds the private key). |

Exit code 78 means a restart cannot help. The systemd unit does not restart
on it (`RestartPreventExitStatus=78`). Docker's restart policy cannot tell
exit codes apart, so under compose stop the service (`docker compose stop`)
until the host is re-enrolled. Every other failure exits 1 and is restarted.

## Connection to the controller

| Log | Meaning | Action |
|---|---|---|
| `registration failed; retrying` / `lease renewal failed; retrying` with `x509` | The controller's certificate is not trusted. There is no way to skip verification. | Install the controller's CA and set `MACHINARCHY_AGENT_SERVER_CA_FILE`, or fix the certificate. |
| the same with `connection refused`, `Unavailable` or a timeout | The server URL is wrong or the controller's agent gateway is unreachable. | Check `MACHINARCHY_AGENT_SERVER_URL`, DNS, firewall and that the controller serves the agent gateway. |
| `command stream ended; reopening` with `PermissionDenied` or `Unauthenticated` | The lease is not live yet, or the agent was revoked. | Usually resolves after the next renewal. A revoked agent stops by itself (exit 78). |
| `command stream ended; reopening` with `ResourceExhausted` | The controller is limiting calls or streams. | Back off is automatic; check the controller's limits if it persists. |

## Running but unhealthy

`machinarchy-agent --healthcheck` prints why. It reads only the state file
(`MACHINARCHY_AGENT_STATE_FILE`, or the default), so it works without the
agent's configuration or secrets. With metrics on, the textfile shows the same
signals over time (`docs/configuration.md#metrics`).

- `not enrolled`: registration has not succeeded yet. Look for
  `retrying` lines with the cause (controller unreachable or not answering, wrong server URL, untrusted certificate).
- `lease expired at ...`: renewals are failing. Check connectivity
  (`disconnected from the controller; reconnecting`, `command stream ended; reopening`) and that the controller's agent
  gateway is ready (`/healthz/ready` on machinarchy).

## Commands are ignored

- `discarding unauthenticated or malformed command`: the command was not
  signed by a pinned controller key. The controller switched to a key this
  agent does not pin: follow the rotation steps below, pinning both keys.
- `discarding command` with an expiry or replay error: clocks differ by more
  than 2 minutes, or the command was already seen. Fix NTP.
- Capability results `rejected` / `capability_unavailable`: the capability is
  not enabled on this host (`service.*` need explicit opt-in and an
  allowlist).

## Controller signing-key rotation

The controller signs everything it sends; agents pin its public key. To
change the key without stopping any agent:

1. **Controller:** create the next key and announce it
   (`MACHINARCHY_AGENT_GATEWAY_NEXT_PUBLIC_KEY`, see machinarchy
   `docs/operations.md`). `GET /api/v1/agents/controller-key` then returns
   both keys. The controller still signs with the current one.
2. **Every agent:** pin both keys and restart:
   `MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY=<current>,<next>`. A restart
   keeps the identity and lease (they are in the state file).
3. **Controller:** sign with the next key (make it current). Agents pinning
   both keys do not notice.
4. **Every agent, later:** pin only the new key.
5. **Controller:** retire the old key.

An agent that skipped step 2 logs `discarding unauthenticated or malformed
command` after step 3; pin both keys on it and restart.

## After a crash

Operations that were running when the agent stopped are reported `unknown`
on restart and never run again: check the host before retrying. Results not
yet acknowledged by the controller are republished.

## Revocation

When an owner revokes the agent, its next lease renewal fails with
`lease_revoked`; the agent clears its identity and stops. It cannot enrol
again with the same key.
