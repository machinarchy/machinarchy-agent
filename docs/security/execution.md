# Remote execution security notes

Remote command and script execution is a high-impact capability. The Agent
registers it only when `MACHINARCHY_AGENT_EXECUTION_ENABLED=true`; the default
is disabled.

## Required controls

- The controller must authenticate the requesting user and authorize the
  workspace, host, operation and requested execution capability before publish.
- NATS ACLs must allow each Agent to subscribe only to its own event subject
  and publish only to its own result subject.
- The Agent must validate the target identity, event envelope, operation
  identifier, strict input schema and operation deadline before execution.
- The OS account running the Agent defines the maximum privilege of the
  command. The Agent does not elevate privileges.
- Current command execution runs under the same OS identity as the Agent.
  Therefore a command may access that identity's readable files, including
  Agent state or credential files when filesystem permissions permit. Filtering
  the child environment does not isolate those files or the Agent process.
  Only enable execution for controller operators trusted with the Agent's
  local authority. A separate privilege boundary is needed for less-trusted
  execution principals.
- Production installations should use TLS and authenticated NATS connections.
- Never place controller tokens, Agent credentials, or long-lived secrets in
  operation arguments or environment values. Use a future secret-injection
  contract with scoped, short-lived material instead.
- Keep operation output bounded and treat stdout/stderr as potentially
  sensitive. The controller must apply access controls to stored job results.
- Record requester, target host, operation ID, capability, transport, timestamps,
  exit status and cancellation outcome in the controller audit trail.

## Execution semantics

`exec.command` executes an argv vector without a shell. `exec.script` executes
script content through the fixed local shell configured by the operator. Both
run with the Agent service user's permissions and the process environment is
constructed from a small safe baseline plus explicitly supplied non-reserved
variables.

On Unix, timeout and cancellation terminate the operation process group. A
successful process exit with code zero is reported as succeeded. A non-zero
exit is reported as failed but retains the bounded stdout/stderr and exit code.

## Deployment warning

The default systemd deployment runs as an unprivileged `machagent` user and
uses filesystem and kernel hardening. Enabling execution does not make commands
administrative. Do not weaken the service sandbox or run the Agent as root
without an explicit threat review and host-specific operational requirement.


## Service management security

`service.status` and `service.control` are separate opt-in capabilities. Both
require a local exact-name `.service` allowlist; control also requires
`MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED=true`. The initial adapter invokes
systemd directly using fixed argument vectors and only supports `start`, `stop`
and `restart`. It does not support arbitrary flags, `sudo`, `enable`, `disable`,
or unit-file mutation. The service-manager child receives a fixed environment
rather than inheriting Agent credentials. OS/systemd/polkit policy remains the
final privilege boundary; the controller must still enforce authorization and
auditing.
