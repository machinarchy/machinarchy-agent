# Initial Threat Model

## Assets

- Controller-assigned agent identity and lease credential.
- NATS authentication material.
- Host-local resources exposed through registered capabilities.
- Operation inputs, outputs and correlation metadata.
- Agent integrity and availability.

## Trust boundaries

- NATS network and all received messages are untrusted until authenticated,
  parsed and validated.
- Controller-issued identity/lease data is authoritative only when received
  over the authenticated broker request/reply path and validated.
- Capability input remains untrusted even after envelope validation.
- Local state files are sensitive and require OS-level access controls.

## Primary threats and controls

| Threat | Controls |
|---|---|
| Credential theft | TLS, protected credential files, no secret logging, rotation/revocation |
| Agent impersonation | Controller-assigned identity, lease token, subject ACLs |
| Cross-agent message delivery | Target agent/host validation and agent-specific subscription subject |
| Malformed/oversized messages | Strict decoder, message limits, bounded queues |
| Duplicate events | Bounded local event ledger and operation identifiers |
| Resource exhaustion | Bounded workers, queues, timeouts, input/output limits |
| Unsafe command execution | Execution disabled by default; explicit controller authorization; fixed local shell; strict inputs; bounded output; deadlines; process-group cancellation; least-privilege OS user |
| Broker outage | Reconnect handling, lease renewal before resuming events |
| Result loss | Documented Core NATS limitation; durable result path remains required |
| Host compromise | Minimal service privileges, filesystem protections and deployment hardening |

## Residual risks

A compromised host can expose its local agent credentials and capabilities.
A compromised controller credential or misconfigured NATS ACL can authorize
malicious work. A process crash can lose in-memory accepted operations or
unpublished results. Core NATS can lose messages during agent disconnection.
These residual risks require controller-side authorization, NATS server
hardening, host isolation and durable delivery design; source-code checks alone
cannot eliminate them.

When command execution is enabled, the controller is effectively trusted to
request any operation permitted to the Agent's OS identity. A compromised
controller or overly broad NATS ACL can therefore cause host-level impact.
Keep execution disabled on hosts that should expose only read-only Agent
capabilities, use a dedicated unprivileged service account by default, and
review any change that grants the Agent additional OS privileges.
