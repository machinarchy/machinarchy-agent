# Phase 1 — Minimal Agent Core and Scope Hardening

## Goal

Keep Machinarchy Agent as a small Linux OS-management runtime. Preserve the
existing registration, lease, NATS/JetStream, durable operation and result
lifecycle; do not add new management domains.

## Supported capability profile

| Capability | Default | Scope |
|---|---|---|
| `runtime.info` | Enabled | Agent version, Go runtime, OS/architecture and registered capabilities |
| `system.facts` | Enabled | Bounded, privacy-conscious host facts |
| `service.status` | Disabled until configured | Read-only status for exact locally allowlisted systemd units |
| `service.control` | Disabled until separately configured | `start`, `stop`, `restart` for exact locally allowlisted systemd units |

Generic `exec.command` and `exec.script` are not registered or implemented by
the Agent. A legacy `MACHINARCHY_AGENT_EXECUTION_ENABLED=true` setting is
rejected at startup with a clear migration error. The shared `protocol/` module may retain `execution/v1` contracts for the
controller's independent SSH transport. That shared contract does not
add shell execution to the Agent.

## Local service policy

To enable read-only service inspection, configure:

```dotenv
MACHINARCHY_AGENT_SERVICE_MANAGEMENT_ENABLED=true
MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED=false
MACHINARCHY_AGENT_SERVICE_ALLOWLIST=nginx.service,ssh.service
```

Service control requires the additional explicit setting:

```dotenv
MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED=true
```

Both capabilities remain subject to the Agent OS identity and local systemd /
Polkit policy. The Agent does not invoke `sudo` or elevate privileges.

## Preserved foundation

- Controller-assigned identity and lease renewal.
- Outbound NATS connection and controller-provisioned JetStream consumer.
- Strict event validation and target/lease checks.
- Bounded event and operation queues, worker count, timeouts and outputs.
- Durable local operation acceptance and result outbox.
- Safe restart recovery: accepted work may resume; interrupted running work
  becomes `unknown` and is not blindly replayed.
- Graceful shutdown and bounded cancellation.

## Explicitly out of scope

Package management, OS upgrades, user/group management, file distribution,
firewall/network management, Kubernetes, dynamic modules, monitoring and
schedules, Windows/macOS service providers, generic shell/command execution,
and controller implementation.

## Acceptance checks

1. Default capability registry contains only `runtime.info` and `system.facts`.
2. Enabling service management with an exact allowlist registers `service.status`.
3. `service.control` is registered only when both service switches are enabled.
4. `runtime.info` reports exactly the capabilities registered on that Agent.
5. Generic command/script capability names are not registered.
6. Existing operation persistence, redelivery, cancellation and recovery tests
   remain green.
7. Formatting, unit tests, race detector, vet and build pass under Go 1.26 CI.

## Verification caveat

The local verification performed for this phase uses the repository's isolated
Go 1.23-compatible test harness and a compile-time NATS API stub because the
current environment does not provide Go 1.26 or live NATS/JetStream. It is not a
substitute for the normal Go 1.26 CI workflow or integration tests.
