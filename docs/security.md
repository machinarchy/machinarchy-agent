# Security model

## Trust boundaries

| Boundary | Control |
|---|---|
| Controller → agent commands | Ed25519 signature by the pinned controller key, kind-bound signing input, addressee and validity-window checks (max 15 min), durable replay ledger, idempotent operation IDs |
| Agent enrollment | One-time enrollment token plus proof of possession of the agent key. The agent accepts only responses signed by the controller key, so a rogue responder cannot assign an identity. |
| Agent → controller | Renewals and results are signed by the agent key recorded at enrollment. No bearer lease token exists. |
| Broker | TLS 1.2+ (required in production), optional private CA and mTLS, exactly one auth method. Per-agent ACLs are still required (see `protocol.md`). |
| Agent → host | Only explicitly enabled capabilities. There is no shell. `systemctl` runs with a fixed argument layout (`--no-ask-password -- <verb> <unit>`), a local allowlist of exact units, the verbs start, stop and restart only, a minimal environment and bounded output. Privilege comes from a host polkit rule, never from the agent. |
| Local state | One state file: 0600 file, 0700 directory, not a symlink, schema-validated, written atomically with fsync. It contains the agent private key. |

## Threats addressed

- **Forged or replayed commands from anyone with broker access.** The agent
  rejects them (signature, ledger, expiry).
- **Credential harvesting on the register subject.** The enrollment token is
  single-use, and the controller must restrict subscriptions to that subject.
  Lease renewal carries no secret.
- **Agent impersonation.** Results and renewals must be signed with the key
  recorded at enrollment.
- **Crash during execution.** An operation that was running when the agent
  stopped is reported `unknown` and never replayed.
- **Resource exhaustion.** The queue, workers, input and output sizes, message
  sizes, the state file and the ledger are all bounded.

## Residual risks

- A compromised controller signing key can command every agent. Keep it in an
  HSM or KMS where possible, and rotate it by redeploying the public key to
  agents.
- Broker ACLs are still needed to limit metadata exposure and denial of service.
- The Docker image uses Debian slim plus the `systemd` package to provide
  `systemctl`. Talking to D-Bus directly would allow a distroless image.

## Reporting

See [SECURITY.md](../SECURITY.md).
