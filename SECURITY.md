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
- Remote command/script execution is disabled by default and runs as the Agent OS user when explicitly enabled.
- Same-identity execution can access files readable by the Agent account; environment filtering is not credential isolation.
- Secrets and sensitive operation output must never be written to logs.
- No inbound listener is required by the current architecture.
- NATS permissions must restrict registration/renewal and agent-specific
  subscribe/publish subjects. A broad `>` permission is not acceptable for an
  ordinary agent credential.

## Known limitations

The initial Core NATS transport does not provide durable replay or explicit
consumer acknowledgements. Local deduplication does not provide exactly-once
execution. A durable operation journal, durable result handling and controller
acknowledgement semantics remain production prerequisites for workflows that
cannot tolerate lost work.
