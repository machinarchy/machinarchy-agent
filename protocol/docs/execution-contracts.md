# Transport-neutral execution contracts

The `execution/v1` package defines the common payloads for operations that can
be delivered over different transports. It is intentionally independent of
NATS, SSH, HTTP and the Agent runtime.

## Command

`CommandRequest` contains one executable, an argv slice, an optional absolute
working directory, an explicit environment map and optional stdin. The
executable and arguments are not concatenated into a shell string. An SSH
executor must preserve argv semantics as closely as its remote execution
mechanism permits; an Agent executor invokes the executable directly.

```json
{
  "command": "/usr/bin/uptime",
  "args": ["-p"],
  "working_directory": "/",
  "environment": {"LANG": "C"}
}
```

## Script

`ScriptRequest` contains an explicit interpreter, script text, positional
arguments, an optional absolute working directory and an explicit environment
map. The Agent checks the requested interpreter against its local allowlist;
the controller cannot bypass that policy or append arbitrary shell flags.
Script content is sent on stdin, not interpolated into a `-c` string. An SSH
executor should enforce an equivalent interpreter policy.

## Result

The agent operation envelope also carries an optional `output_limit_bytes`
field. Zero means the Agent's configured maximum. A non-zero value must be
within the Agent's configured range and is enforced for that operation.

`Result` normalizes stdout, stderr, exit code, duration and output truncation
flags. A non-zero exit code is a failed operation, but the result must retain
stdout/stderr so the operator can diagnose the failure.

## Semantics to preserve across transports

- Do not merge stdout and stderr.
- Do not discard output on non-zero exit.
- Apply the operation timeout and cancellation to the underlying process.
- Bound input, output and execution concurrency.
- Do not silently elevate OS privileges.
- Do not automatically retry using another transport after the command may
  have started. An unknown outcome is not a safe retry condition.
- Never claim exactly-once execution based on transport delivery alone.


## Agent operation result acknowledgement (protocol v2)

An Agent retains each terminal result in its local outbox until the controller
confirms durable persistence by publishing an
`operation.result.acknowledged` event for the same `operation_id`. The controller
must upsert results idempotently before acknowledging. Result publication may
repeat after reconnects; consumers must not treat duplicate publication as a
second execution.

The `unknown` status means the Agent process restarted after persisting
`running` but cannot establish whether the external side effect completed. It
is not equivalent to failure and must not trigger blind automatic retry or
transport fallback.
