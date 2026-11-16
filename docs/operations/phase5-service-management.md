# Phase 5 — bounded service management

## Scope

The Agent adds two opt-in capabilities backed by the Linux systemd service
manager:

| Capability | Input | Behavior |
|---|---|---|
| `service.status` | `{"service":"nginx.service"}` | Read a normalized subset of unit state |
| `service.control` | `{"service":"nginx.service","action":"restart"}` | Execute exactly one of `start`, `stop`, or `restart` |

The shared, transport-neutral request/response types live in
`machinarchy-protocol/services/v1` (protocol module v0.2.3). The Agent protocol
envelope remains version 2.

This is not yet a desired-state system. It does not implement `enable`,
`disable`, `reload`, unit-file changes, dependency ordering, health checks,
`service.ensure`, or Windows/macOS service managers.

Host service management is currently supported only by the native Linux Agent
when the Agent OS identity and local systemd/Polkit policy permit the action.
The current Docker image is not a host systemd integration and must keep both
service-management settings disabled. Docker host integration is a separate
planned deployment phase.

## Local opt-in and allowlist

All service operations are disabled by default. To enable read-only status on a
Linux host, configure:

```dotenv
MACHINARCHY_AGENT_SERVICE_MANAGEMENT_ENABLED=true
MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED=false
MACHINARCHY_AGENT_SERVICE_ALLOWLIST=nginx.service,ssh.service
MACHINARCHY_AGENT_SYSTEMCTL_PATH=/usr/bin/systemctl
```

To allow lifecycle actions, separately set:

```dotenv
MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED=true
```

The allowlist is mandatory, exact-match, comma-separated and limited to 64
systemd `.service` unit names. The initial safe-name subset permits letters,
digits, dots, underscores, hyphens and `@`; wildcard matching and arbitrary
controller-supplied arguments are not supported. The Agent verifies the allowlist again at execution time. A service not listed locally is rejected even if the
controller sends a valid operation.

`service.control` is registered only when both switches are enabled. `runtime.info`
reports only the capabilities that are actually registered.

## Execution and security boundary

- The Agent invokes the configured absolute `systemctl` executable directly
  with an argument vector. It never invokes a shell.
- The only control verbs are `start`, `stop` and `restart`; the unit name must
  match the local allowlist.
- Status output is parsed into a small schema and is bounded. Unexpected,
  duplicate or missing systemd properties are rejected.
- The operation dispatcher supplies the operation deadline and cancellation
  context. Cancellation terminates the `systemctl` child process. Concurrent
  lifecycle requests for the same unit are serialized inside one Agent process;
  different units may be controlled concurrently.
- The service-manager child receives a small fixed environment. Agent NATS
  credentials, enrollment tokens and arbitrary parent environment variables
  are not inherited.
- The Agent never invokes `sudo`, changes UID, or elevates privileges. The
  local OS identity, systemd/polkit policy and filesystem policy determine
  whether an action succeeds. A status request can work while a control request
  is denied by the OS.
- The controller must independently authorize the user, workspace, host,
  operation and capability before publishing a request. Agent-side allowlisting
  is a second boundary, not a replacement for controller RBAC.

Do not run the network-facing Agent as root merely to make service actions work.
Do not add broad polkit rules that permit arbitrary systemd unit control. The
current default hardened `machagent` service account may not be permitted to
control system services; that is an expected and safe failure until an
administrator defines a narrow host-local policy.

## Operation semantics

A successful control response means systemd accepted and completed the requested
job; it does not prove the application is healthy. Follow up with
`service.status` or a future application-specific health check.

The operation dispatcher durably records accepted operations and deduplicates
operation IDs. However, restarting a host service is an external side effect.
If the Agent or host crashes after systemd performs the action but before the
result is durably acknowledged by the controller, the operation may be reported
as `unknown`. Do not blindly retry an `unknown` restart. Inspect the service and
create a new operation after reconciling the actual state.

## Rollout checklist

1. Publish and consume `machinarchy-protocol` v0.2.3 in both controller and Agent.
2. Ensure the controller's capability authorization and operation audit model
   understands `service.status` and `service.control`.
3. Keep service management disabled in the baseline Agent deployment.
4. Pilot read-only status with a small exact allowlist.
5. Test control under the real Agent OS account and the intended local systemd /
   polkit policy. Do not assume root privileges.
6. Test cancellation, NATS disconnect, Agent restart, duplicate delivery and
   host reboot around a `restart` operation.
7. Enable `service.control` only on hosts with an approved local policy.

## Current support

| Platform | Status |
|---|---|
| Linux + systemd | Initial backend implemented; requires deployment verification |
| Linux without systemd | Unsupported |
| Windows Service Control Manager | Not implemented |
| macOS launchd | Not implemented |

Hosts without a supported Agent service manager can continue to use the
controller's SSH transport for separately authorized tasks.
