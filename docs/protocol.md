# Agent protocol v1

The contract lives in [`protocol/agentv1`](../protocol/agentv1) and is the single source
of truth for both sides. It depends only on the Go standard library, so the
controller can import `github.com/machinarchy/machinarchy-agent/protocol/agentv1`
directly. The golden fixtures in `protocol/agentv1/testdata` pin the wire format.

## Transport

The agent reaches the controller only through its **agent gateway**, a gRPC
service over TLS (ADR-059 in the machinarchy repository). The agent holds a
server URL, its enrolment token and its own keys; it knows no broker address
and holds no broker credential. What the controller uses internally to queue
commands is not part of this contract.

Service `machinarchy.agent.v1.AgentGateway` (constants in
[`protocol/agentv1/gateway.go`](../protocol/agentv1/gateway.go)). Messages are
the signed envelopes below, carried as opaque bytes with the content subtype
`agentv1` (no protobuf schema), so authenticity never depends on the transport.

| Method | Kind | Carries |
|---|---|---|
| `Register` | unary | signed `register.request`, answered with a signed `register.response` |
| `Renew` | unary | signed `lease.renew.request`, answered with a signed `lease.renew.response` |
| `SendResult` | unary | signed `result`, answered with an empty message; needs a live lease |
| `Commands` | bidirectional stream | the agent opens it with a signed `lease.renew.request` naming its current lease (sent within the clock skew); the controller then sends one signed `command` at a time and waits for the agent to send the command ID back as an acknowledgement |

Behaviour the agent relies on:

- A command is delivered **at least once, in order, one at a time** per agent.
  A command whose acknowledgement does not arrive (stream lost, agent
  restart) is delivered again on the next stream. Commands queued while the
  agent is offline are delivered when it reconnects.
- A stream is bound to the agent identity proven by its opening message. A
  copied lease ID is useless without the agent key. The controller closes the
  stream when the lease expires or the agent is revoked.
- Failures carry a generic gRPC status and no detail: `Unauthenticated` or
  `PermissionDenied` (not authorised, lease not live), `InvalidArgument`,
  `ResourceExhausted` (rate or stream limits), `Unavailable`.
- The agent verifies the controller's TLS certificate (system roots, plus
  `MACHINARCHY_AGENT_SERVER_CA_FILE`). There is no option to skip it.

## Envelope and signatures

Every message is an `Envelope`:

```json
{"v":1,"kind":"command","body":"<base64 of the exact JSON body>","sig":"<base64 Ed25519 signature>"}
```

The signature covers `"machinarchy-agent/v1" 0x00 kind 0x00 body`. Because the
kind is part of the signed input, a signature for one message type cannot be
replayed as another. Decoding is strict: unknown fields, trailing data and
oversized messages are rejected.

| Message | Signed by | Verified with |
|---|---|---|
| `register.request` | new agent key | the key inside the body (proof of possession), via `OpenRegisterRequest` |
| `register.response`, `lease.renew.response`, `command` | controller key (`Seal`, or `SealWith` for a key in a KMS) | the controller public key(s) pinned in agent config, via `OpenAny`: one key, or two during a rotation |

Signing-key rotation needs no wire change: the envelope carries no key ID,
and an agent pinning two keys accepts a message that either verifies.
`SealWith` takes any Ed25519 `crypto.Signer` and checks the returned signature
before sending, so a faulty KMS cannot emit messages agents reject.
| `lease.renew.request`, `result` | agent key | the key recorded at registration, via `OpenFromAgent` |

## Flows

1. **Enrollment.** On first start the agent generates an Ed25519 key. It sends a
   `RegisterRequest` that carries a one-time enrollment token, its public key,
   its version and its capabilities. Retries reuse the same `request_id`, so the
   controller must treat the request as idempotent. The controller verifies the
   token, records the key and replies
   with `agent_id`, `host_id` and a `Lease`.
2. **Lease.** The agent renews every `heartbeat_interval_ms`. A response with
   `unknown_agent` or `lease_revoked` makes the agent delete its identity and
   stop; it needs a new enrollment token to come back. While the lease is
   expired, the agent leaves commands unacknowledged and reopens the command
   stream once the lease is renewed.
3. **Commands.** `operation.request`, `operation.cancel` and `result.ack`:
   - Each command has a unique `command_id`, the agent's `agent_id`, and a
     validity window of at most 15 minutes.
   - The controller de-duplicates on `command_id` when queueing.
   - The agent drops replays through a durable ledger and treats `operation_id`
     as the idempotency key.
4. **Results.** The agent signs and sends with `SendResult`:
   - `accepted` as a best-effort notice;
   - one terminal status: `succeeded`, `failed`, `rejected`, `timed_out`,
     `cancelled` or `unknown`.

   It resends terminal results until it receives a `result.ack` command for
   that `operation_id`. `unknown` means the agent stopped mid-execution. The side
   effect may or may not have happened, so it must never be retried
   automatically.

## Capabilities

| Name | Input | Output | Enabled |
|---|---|---|---|
| `runtime.info` | `{}` | `RuntimeInfo` | always |
| `system.facts` | `{}` | `SystemFacts` | always |
| `service.status` | `ServiceStatusRequest` | `ServiceStatus` | `MACHINARCHY_AGENT_SERVICE_MANAGEMENT_ENABLED` + allowlist |
| `service.control` | `ServiceControlRequest` (`start`, `stop` or `restart`) | `ServiceControlResponse` | additionally `MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED` |

There is no generic command or script execution.

## What the controller must implement

machinarchy implements this in `internal/agentgateway`.
`internal/fakecontroller` is a reference gateway (in-process gRPC) for this
repository's integration tests. A controller must:

- **Keys and tokens:**
  - Hold an Ed25519 signing key, and distribute its public key to agents.
  - Issue single-use, short-lived enrollment tokens and compare them in
    constant time.
- **Agent records:** persist `agent_id`, `host_id`, the public key, the lease
  and the registration `request_id` for idempotent retries.
- **Gateway service:** serve `machinarchy.agent.v1.AgentGateway` over TLS
  with the four methods above, rate-limit unauthenticated calls, bound the
  number of open streams, and cap message sizes.
- **Command queue:** a durable queue per agent with at-least-once delivery in
  order; an agent can never receive another agent's commands.
- **Responders and results:**
  - Answer register and renew requests with signed responses.
  - Verify results with `OpenFromAgent` (the signed agent ID is the identity;
    require a live lease), upsert by `operation_id`, then send `result.ack`.
