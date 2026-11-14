# Agent Phase 2 — Identity, Registration, and Leases

## Status

**Agent-side implementation drafted; controller-side protocol is not implemented or verified.**

The NATS subjects and JSON payloads in this document are a proposed version-1 request/reply contract for the next control-plane implementation. They must be reconciled with the accepted ADR-050 and promoted into the shared `internal/eventing` contracts before production use. Do not assume the current API or control manager already handles these subjects.

## Scope

This phase adds the agent-side lifecycle required to establish a control-plane-issued identity:

1. Persist a stable enrollment request ID before attempting registration.
2. Submit a one-time enrollment token over the authenticated broker connection.
3. Receive agent and host IDs assigned by the control plane.
4. Receive a lease ID and lease credential.
5. Persist identity and lease state locally with restrictive filesystem permissions.
6. Renew the lease periodically using request/reply so the agent can observe acceptance or rejection.
7. Renew immediately after broker reconnection.
8. Retry transient failures only while the last acknowledged lease remains valid.
9. Stop when the control plane permanently rejects the identity or the lease expires.

No command execution, job subscription, or arbitrary shell capability is included.

## Proposed NATS request/reply contract

Subjects are literal and versioned:

- `machinarchy.v1.agent.register`
- `machinarchy.v1.agent.lease.renew`

Each request and response carries `protocol_version: 1` and the request's `request_id`. The agent rejects mismatched request IDs and unsupported protocol versions.

### Registration request

```json
{
  "protocol_version": 1,
  "request_id": "<stable-idempotency-key>",
  "enrollment_token": "<one-time-enrollment-token>",
  "hostname": "worker-01",
  "agent_version": "0.2.0",
  "operating_system": "linux",
  "architecture": "amd64"
}
```

The control plane must resolve the enrollment token to the permitted workspace and host. The agent does not choose its own `agent_id` or `host_id`.

### Registration response

```json
{
  "protocol_version": 1,
  "request_id": "<same-request-id>",
  "agent_id": "<control-plane-assigned-id>",
  "host_id": "<control-plane-assigned-host-id>",
  "lease_id": "<lease-id>",
  "lease_token": "<high-entropy-lease-credential>",
  "lease_expires_at": "2026-11-14T00:00:00Z",
  "heartbeat_interval_ms": 15000
}
```

### Lease renewal request

```json
{
  "protocol_version": 1,
  "request_id": "<unique-request-id>",
  "agent_id": "<assigned-agent-id>",
  "host_id": "<assigned-host-id>",
  "lease_id": "<lease-id>",
  "lease_token": "<current-lease-credential>",
  "agent_version": "0.2.0",
  "hostname": "worker-01",
  "sent_at": "2026-11-13T22:00:00Z"
}
```

### Lease renewal response

```json
{
  "protocol_version": 1,
  "request_id": "<same-request-id>",
  "lease_expires_at": "2026-11-14T00:00:00Z",
  "heartbeat_interval_ms": 15000,
  "lease_token": "<optional-rotated-credential>"
}
```

The control plane may rotate `lease_token`. The agent validates and persists a rotated value before continuing.

### Error response

```json
{
  "protocol_version": 1,
  "request_id": "<same-request-id>",
  "error": {
    "code": "enrollment_expired",
    "message": "Enrollment token is no longer valid",
    "retryable": false
  }
}
```

Error messages are not logged by the agent. The bounded, sanitized error code is safe to report. A non-retryable rejection stops the agent; a retryable failure is retried only while the last acknowledged lease remains valid.

## Idempotency and persistence

Before registration, the agent writes a pending `request_id` to its identity file. If the request is processed by the controller but the reply is lost, the next attempt reuses the same ID. The controller must enforce idempotency for `(enrollment token, request_id)` and return the original registration result rather than consuming the enrollment token twice or creating a second agent.

After successful registration, the file contains:

- schema version
- control-plane-assigned agent ID
- control-plane-assigned host ID
- lease ID
- lease token
- lease expiration
- heartbeat interval

The enrollment token is never written to disk. The lease token is a bearer credential and is stored in a `0600` file beneath a `0700` directory. The file is written atomically using a temporary file, `fsync`, and rename. This is filesystem access control, not encryption at rest; protect the agent's data volume and operating-system account accordingly.

## Configuration

```bash
# Existing Phase 1 broker connection settings remain required.
export MACHINARCHY_AGENT_NATS_URL='tls://nats.example.net:4222'
export MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE='/etc/machinarchy/agent.creds'

# Supply only for first enrollment. Do not put this in a shared image.
export MACHINARCHY_AGENT_ENROLLMENT_TOKEN='...'

# Persistent identity / lease state.
export MACHINARCHY_AGENT_STATE_FILE='/var/lib/machinarchy-agent/identity.json'
export MACHINARCHY_AGENT_VERSION='0.2.0'

# Optional subject and timeout overrides.
export MACHINARCHY_AGENT_REGISTER_SUBJECT='machinarchy.v1.agent.register'
export MACHINARCHY_AGENT_LEASE_RENEW_SUBJECT='machinarchy.v1.agent.lease.renew'
export MACHINARCHY_AGENT_REQUEST_TIMEOUT='5s'
```

The default production identity path is `/var/lib/machinarchy-agent/identity.json`; development/test defaults to `./data/agent/identity.json`.

## Controller requirements before end-to-end use

The control plane must implement and test both request/reply handlers. In particular it must:

- validate and consume the enrollment token atomically;
- bind the agent to an authorized host and workspace;
- make registration idempotent using `request_id`;
- generate cryptographically random lease credentials;
- store only a verifier/hash of lease credentials where feasible;
- enforce lease ownership and expiration on every renewal;
- rotate the lease credential safely if returning a new one;
- reject revoked agents and expired enrollment tokens as non-retryable errors;
- bound message sizes and validate protocol version;
- configure NATS subject ACLs so an agent cannot register, renew, or impersonate another agent outside its authorized scope;
- audit enrollment, lease renewal, revocation, and administrative changes without recording credentials.

The controller must become the authoritative source of agent status. A successful TCP/NATS connection is not evidence of a valid registered agent.

## Operational behavior

- Initial registration requires `MACHINARCHY_AGENT_ENROLLMENT_TOKEN` only when no registered identity is present.
- An existing identity is renewed at startup before the agent enters its session loop.
- The agent renews on the server-provided heartbeat interval and immediately after broker reconnection.
- Transport errors and explicitly retryable control-plane errors use bounded exponential backoff.
- A failed renewal is retried only while the last acknowledged lease is still valid.
- Non-retryable rejections and lease expiration stop the agent; they do not silently create a new identity.
- Graceful process shutdown lets the broker connection drain. The lease naturally expires unless a future protocol adds an explicit release operation.

## Known limitations

- The wire contract is a proposal and has no matching controller-side implementation in this bundle.
- The current agent NATS adapter is still separate from the repository's existing `internal/broker` package. Reconcile those abstractions before merging.
- The current NATS connection authentication is separate from the agent lease credential. A future ADR may define per-agent NATS credentials or NATS account permissions.
- Local identity state is permission-protected but not encrypted at rest.
- Agent event subscriptions, durable work delivery, operation dispatch, execution, and result reporting remain future phases.
