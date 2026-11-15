# Agent-side remote execution

## Purpose and boundary

The Agent is one execution transport in Machinarchy's hybrid execution model.
It receives controller-authorized operations and runs them on the local host.
The controller owns inventory, targeting, transport selection, authorization,
job scheduling, and job state. The Agent owns local process execution and
returns a normalized result.

The `machinarchy-protocol/execution/v1` package defines transport-neutral
`CommandRequest`, `ScriptRequest`, and `Result` contracts. The controller's SSH
executor and the Agent executor must use these same payload and result shapes.
The Agent protocol envelope still carries the capability name and operation
input; the execution payload itself is not tied to NATS or SSH.

## Capabilities

| Capability | Purpose | Input |
|---|---|---|
| `exec.command` | Execute one program with an argument vector; no shell parsing | `executionv1.CommandRequest` |
| `exec.script` | Execute script text using the locally configured shell | `executionv1.ScriptRequest` |

`exec.command` never concatenates arguments into a shell command. The executable
is resolved either from an absolute path or from a fixed Agent PATH. Relative
paths containing directory separators are rejected.

`exec.script` always uses the Agent's configured shell path. The controller
cannot choose an interpreter or shell flags. Script content is passed over
stdin (`sh -s -- ...`), never interpolated into `sh -c`.

Example command operation payload (the `output_limit_bytes` and timeout belong
to the shared Agent operation envelope, not to the command input):

```json
{
  "operation_id": "op-123",
  "capability": "exec.command",
  "timeout_ms": 30000,
  "output_limit_bytes": 65536,
  "input": {
    "command": "/usr/bin/uptime",
    "args": ["-p"],
    "working_directory": "/"
  }
}
```

Example script input:

```json
{
  "interpreter": "/bin/sh",
  "script": "set -eu\necho \"deploying $1\"\n",
  "args": ["release-42"],
  "working_directory": "/opt/my-app"
}
```

The requested script interpreter must match the Agent's locally configured
`MACHINARCHY_AGENT_EXECUTION_SHELL` path. This initial implementation supports
one explicitly configured interpreter per Agent.

Both capabilities accept an optional absolute working directory, a bounded
explicit environment map, and arguments. The Agent does not inherit its own
process environment into child processes. In particular, NATS credentials,
enrollment tokens, and `MACHINARCHY_*` process settings are not passed to child
commands. The default PATH, HOME, locale, and temporary-directory settings are
constructed locally. PATH/HOME and dynamic-loader variables are protected from
request overrides. This environment filtering is not a privilege boundary: a
child running as the same OS user may still access files and process resources
available to that user, including Agent state or credential files if their
permissions allow it.

## Opt-in and local privileges

Command execution is disabled by default. Enable it explicitly on a host with:

```dotenv
MACHINARCHY_AGENT_EXECUTION_ENABLED=true
MACHINARCHY_AGENT_EXECUTION_SHELL=/bin/sh
```

A registered capability means the Agent can execute the requested work; it
does not grant additional OS permissions. A process runs with exactly the
permissions of the OS account running `machinarchy-agent`. The Agent must not
silently invoke `sudo`, change identity, or elevate privileges.

The default systemd unit runs as the dedicated `machagent` user with a
restrictive sandbox. This is safer for general remote execution but may not be
able to restart system services, install packages, or modify protected paths.
Commands running as the same account can access that account's readable files;
therefore the current same-identity execution model is intended only for
controller operators trusted with the Agent account's local authority. It does
not isolate Agent credentials from an authorized command. A separate,
privilege-separated executor is required before treating scripts from a less
trusted administrative boundary as safe.

Operators who require privileged administration must make a conscious host
policy decision about the service account and sandbox. Do not solve this by
silently running every Agent as root.

## Resource and process controls

- Requests are strict JSON objects; unknown fields are rejected.
- Command argument count, individual argument size, environment entry count,
  environment size, stdin size, and script size are bounded.
- The existing dispatcher applies per-operation deadlines, cancellation,
  bounded concurrency, and bounded queue admission.
- stdout and stderr are captured separately and bounded. Truncation is
  reported in the common result contract.
- A non-zero process exit returns the stdout/stderr and exit code while marking
  the operation as failed.
- On Unix, each operation starts in a separate process group; cancellation kills
  the process group so shell descendants do not remain running. Windows currently
  cancels the direct child only; process-tree termination needs a Windows Job
  Object implementation before Windows should be treated as a production target.
- Child processes do not inherit Agent secrets from the parent environment.

## Controller integration still required

Before automatic Agent transport selection is enabled, the controller must:

1. Consume the capability list exposed by Agent metadata and persist it with the
   Agent session/heartbeat.
2. Send only capabilities supported by the target Agent.
3. Apply workspace authorization and host-level execution policy before
   dispatching work.
4. Use the same execution request/result contracts for SSH and Agent.
5. Preserve operation IDs and avoid retrying an operation through another
   transport when the first execution may already have started.
6. Surface execution transport, exit code, stdout/stderr, and truncation in the
   common job result model.

## Delivery semantics

This change does not add durable delivery. With the current NATS Core path, an
in-flight operation or unpublished result can still be lost on disconnect or
process crash. It also does not provide exactly-once process execution. The
controller must treat an interrupted result as potentially unknown rather than
blindly resubmitting the operation over SSH.
