# Repository instructions

## Project boundary

This repository contains the Machinarchy Agent runtime only. It must not own
controller persistence, workspace/RBAC policy, host inventory, enrollment
issuance, authoritative leases, or controller-side operation state.

Shared wire contracts belong to `github.com/machinarchy/machinarchy-protocol`.
Do not duplicate those DTOs inside this repository or import packages from the
controller's `internal/` tree.

## Current Agent product scope

The current Agent MVP is a lightweight Linux OS-management runtime. Its only
supported capability names are `runtime.info`, `system.facts`, and opt-in
`service.status` / `service.control` for exact local systemd unit allowlists.
Do not add generic `exec.command` / `exec.script`, package management, file
management, users/groups, firewall, network configuration, Kubernetes, or
monitoring features without an explicit product-scope decision. The shared
protocol may retain contracts used by the controller's independent SSH
transport; that does not authorize registering those capabilities in this Agent.

## Engineering requirements

- Keep `cmd/machinarchy-agent` thin; lifecycle logic belongs under `internal/agent`.
- Treat all broker input as untrusted.
- Never implement unrestricted remote shell execution as a default capability.
- Every capability must be explicitly registered, validated, bounded, and cancellable.
- Bound queues, worker counts, message sizes, operation timeouts, and output sizes.
- Do not log credentials, tokens, private keys, or sensitive operation payloads.
- Use `context.Context` for cancellation and deadlines.
- Preserve the controller as the authority for agent identity and lease validity.
- Do not claim durable delivery or exactly-once execution when using Core NATS.
- Do not add a broker, HTTP server, or persistence dependency without an ADR.
- Avoid unrelated refactoring and unnecessary abstractions.
- Docker Engine deployment must remain non-root, non-privileged, without the
  Docker socket, host PID/network namespaces, or a host-root filesystem mount.
- The default Docker profile must not mount the host system D-Bus socket. Host
  systemd access is an explicit Compose overlay backed by a host Polkit rule.

## Change log requirement

For every project change, update `ChangeLogAI.md` with the date, files changed,
reason, and verification performed. Also update `CHANGELOG.md` for user-visible
releases.

## Required checks

Before considering a change complete, run:

```bash
make fmt-check
make test
make test-race
make vet
make build
```

For integration changes, run real NATS tests and controller/agent end-to-end
tests. The isolated test harness is not a substitute for those tests.
