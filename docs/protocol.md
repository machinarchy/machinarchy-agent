# Agent protocol v1

The contract lives in [`protocol/agentv1`](../protocol/agentv1) and is the single source
of truth for both sides. It depends only on the Go standard library, so the
controller can import `github.com/machinarchy/machinarchy-agent/protocol/agentv1`
directly. The golden fixtures in `protocol/agentv1/testdata` pin the wire format.

## Subjects

| Subject | Direction | Pattern |
|---|---|---|
| `machinarchy-agent.v1.register` | agent → controller | request/reply |
| `machinarchy-agent.v1.lease.renew` | agent → controller | request/reply |
| `machinarchy-agent.v1.agent.<agent_id>.cmd` | controller → agent | JetStream stream `MACHINARCHY_AGENT_COMMANDS`, durable pull consumer `agent-<agent_id>` |
| `machinarchy-agent.v1.agent.<agent_id>.result` | agent → controller | core NATS publish |

The root is deliberately outside the controller's internal `machinarchy.>`
namespace. A controller stream on `machinarchy.>` cannot capture agent
request/reply traffic or credentials.

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
| `register.response`, `lease.renew.response`, `command` | controller key | the controller public key pinned in agent config |
| `lease.renew.request`, `result` | agent key | the key recorded at registration, via `OpenFromAgent` |

## Flows

1. **Enrollment.** On first start the agent generates an Ed25519 key. It sends a
   `RegisterRequest` that carries a one-time enrollment token, its public key,
   its version and its capabilities. Retries reuse the same `request_id`, so the
   controller must treat the request as idempotent. The controller verifies the
   token, records the key, provisions the agent's durable consumer and replies
   with `agent_id`, `host_id` and a `Lease`.
   When NATS runs in operator (JWT) mode, the response also carries
   `nats_user_jwt` (see [NATS credentials](#nats-credentials)).
2. **Lease.** The agent renews every `heartbeat_interval_ms`. A successful
   response may carry a fresh `nats_user_jwt`. A response with
   `unknown_agent` or `lease_revoked` makes the agent delete its identity and
   stop; it needs a new enrollment token to come back. While the lease is
   expired, the agent naks commands instead of executing them.
3. **Commands.** `operation.request`, `operation.cancel` and `result.ack`:
   - Each command has a unique `command_id`, the agent's `agent_id`, and a
     validity window of at most 15 minutes.
   - Publish with `Nats-Msg-Id = command_id` so the broker de-duplicates.
   - The agent drops replays through a durable ledger and treats `operation_id`
     as the idempotency key.
4. **Results.** The agent signs and publishes:
   - `accepted` as a best-effort notice;
   - one terminal status: `succeeded`, `failed`, `rejected`, `timed_out`,
     `cancelled` or `unknown`.

   It republishes terminal results until it receives a `result.ack` command for
   that `operation_id`. `unknown` means the agent stopped mid-execution. The side
   effect may or may not have happened, so it must never be retried
   automatically.

## NATS credentials

Signatures stop forgery; NATS permissions stop agents from reading or
disrupting each other's traffic. With NATS in operator (JWT) mode:

- **Bootstrap credential.** Before enrolment, the agent connects with a shared
  credential (`MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE`, or a token or
  user/password) that has `BootstrapPermissions`. It can only publish
  `register` and `lease.renew` requests and read replies under `_INBOX.>`.
  Requests are signed, so this credential cannot act as any agent.
- **Per-agent JWT.** The controller holds a signing key of the NATS account
  and returns `nats_user_jwt` in register and renew responses. The JWT's
  subject is the agent's own Ed25519 identity key encoded as a NATS user nkey,
  so no extra secret is stored on the host. Its permissions are
  `AgentPermissions(agent_id)`:
  - publish to register, renew, the agent's own result subject, and the
    JetStream info, next and ack subjects of its own consumer only;
  - subscribe only to its own inbox root, `_INBOX_AGENT.<agent_id>.>`.
- **Reconnect.** After enrolment the agent reconnects with the JWT, signing
  the server nonce with its identity key, and uses
  `AgentInboxPrefix(agent_id)` for replies. On every reconnect it uses the
  latest JWT from its state file. If the stored JWT has expired at startup, it
  connects with the bootstrap credential, renews, and reconnects.
- **Revocation.** The controller stops issuing JWTs to a revoked agent. The
  server closes the agent's connection when its current JWT expires (the TTL is
  set on the controller, 1 hour by default); the agent has already stopped on
  `lease_revoked`.
- **Controller.** The controller uses `ControllerInboxPrefix`, so enrolling
  agents cannot read its replies.

**Compatibility.** `nats_user_jwt` is additive and omitted unless the
controller issues JWTs. Agents built before it reject responses that carry the
field (strict decoding), so upgrade agents before enabling JWT issuance on the
controller. Without JWT issuance nothing changes on the wire, so
`ProtocolVersion` stays 1.

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
`internal/fakecontroller` is a reference controller for this repository's
integration tests. A controller must:

- **Keys and tokens:**
  - Hold an Ed25519 signing key, and distribute its public key to agents.
  - Issue single-use, short-lived enrollment tokens and compare them in
    constant time.
- **Agent records:** persist `agent_id`, `host_id`, the public key, the lease
  and the registration `request_id` for idempotent retries.
- **Command stream and consumers:**
  - Create the stream `MACHINARCHY_AGENT_COMMANDS` on
    `machinarchy-agent.v1.agent.*.cmd`, with a duplicate window of at least
    15 minutes.
  - Create a durable consumer per agent: `agent-<agent_id>`, explicit ack, and
    `FilterSubject` set to that agent's command subject.
- **Responders and results:**
  - Answer register and renew requests with signed responses.
  - Verify results with `OpenFromAgent`, check that the subject matches the
    signer, upsert by `operation_id`, then publish `result.ack`.
- **Broker permissions:** in operator mode, issue each agent a NATS user JWT
  with `AgentPermissions` (see [NATS credentials](#nats-credentials)), and give
  the shared bootstrap credential only `BootstrapPermissions`.
