# Repository instructions

## Project boundary

This repository contains the Machinarchy Agent runtime and the agent wire
contract. It must not own controller persistence, workspace/RBAC policy, host
inventory, enrollment issuance or authoritative leases.

`protocol/agentv1` is the single source of truth for the controller ↔ agent
contract. It must depend only on the Go standard library. Any wire change must:
- update the golden fixtures (`go test ./protocol/... -run Golden -update`);
- update `docs/protocol.md`;
- bump `ProtocolVersion` if it is not backward compatible.

## Product scope

The only capabilities are `runtime.info`, `system.facts`, and opt-in
`service.status` / `service.control` for exact local systemd unit allowlists
(start, stop, restart only). Do not add generic command or script execution,
package or file management, users, firewall, network or Kubernetes features
without an explicit product decision recorded as an ADR.

## Package layout

| Package | Responsibility |
|---|---|
| `cmd/machinarchy-agent` | Composition root only |
| `protocol/agentv1` | Wire contract, signing, validation |
| `internal/config` | Environment loading and validation |
| `internal/state` | The one durable state file |
| `internal/app` | NATS connection, enrollment, lease, command consumer |
| `internal/dispatch` | Execution, recovery, result outbox |
| `internal/capabilities` | Host capabilities |
| `internal/fakecontroller` | Reference controller for integration tests |

Do not create a catch-all package. Keep packages small and avoid dependency
cycles.

## Engineering requirements

- Treat all broker input as untrusted. Verify signatures before acting.
- Every capability must be registered explicitly, validated, bounded and
  cancellable.
- Bound queues, worker counts, message sizes, timeouts and output sizes.
- Never log credentials, tokens, private keys or operation payloads.
- Never replay an operation that may have started; report it as `unknown`.
- Do not add a broker, HTTP server or persistence dependency without an ADR.
- Docker deployment stays non-root and non-privileged: no Docker socket, no
  host PID or network namespaces, no host-root mount. The host D-Bus socket is
  mounted only by the explicit systemd overlay.

## Change log requirement

For every change, update `ChangeLogAI.md` with the date, files changed, reason
and the verification actually performed. Also update `CHANGELOG.md` for
user-visible changes.

## Required checks

```bash
make check
```

This runs fmt-check, tidy-check, vet (host, linux/amd64, linux/arm64),
test-race (including integration tests against an embedded nats-server), lint,
build and deployment-check. Also run `make vuln` before a release.
