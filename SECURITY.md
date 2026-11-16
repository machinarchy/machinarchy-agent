# Security Policy

## Supported versions

Security fixes are applied to the latest supported release. Upgrade to the
latest compatible Agent release unless a security advisory states otherwise.

## Reporting a vulnerability

Do not report security vulnerabilities in public issues. Use GitHub's private
vulnerability reporting for this repository, or contact the maintainers
through the private security contact configured in the GitHub repository.

Include the affected version, deployment context, reproduction steps and
potential impact. Do not include real credentials, private keys or customer
payloads.

## Security expectations

- Production broker connections require TLS and authentication.
- Agent credentials and identity state must be readable only by the service
  account (normally mode `0600`; parent state directory `0700`).
- Agent messages are untrusted until protocol, identity, host, lease and
  capability checks succeed.
- The controller remains authoritative for identity and lease validity.
- Capabilities must be explicitly registered and bounded.
- The Agent does not expose generic remote command or shell-script execution.
- Service management is opt-in, exact-allowlist based and constrained by the
  Agent OS identity and host systemd/Polkit policy.
- Secrets and sensitive operation output must never be written to logs.
- No inbound listener is required by the current architecture.
- NATS permissions must restrict registration/renewal and agent-specific
  subscribe/publish subjects. A broad `>` permission is not acceptable for an
  ordinary agent credential.

## Known limitations

Inbound commands use a durable JetStream pull consumer with explicit
acknowledgements, but delivery remains at-least-once. Registration/lease
request/reply and Agent result publication use Core NATS; terminal results are
retried from the local result outbox until the controller sends an
acknowledgement after committing persistence. Local deduplication and broker
redelivery do not provide exactly-once execution. A crash after an external
side effect can result in `unknown`, which must not be automatically retried
without an explicit policy.
