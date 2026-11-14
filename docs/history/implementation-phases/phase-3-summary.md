# Machinarchy Agent — Phase 3 Bundle

This bundle includes the complete agent source from Phases 1–3.

## New Phase 3 files

- `internal/agent/events.go` — versioned agent event envelope, decoding, validation, event subject construction, event processor, and validation-only handler.
- `internal/agent/event_ledger.go` — private atomic file-backed deduplication ledger with a bounded 4,096-event window.
- `internal/agent/events_test.go` — event envelope, target, timestamp, payload, deduplication, and ledger security tests.
- `docs/agent/phase-3-event-subscription-validation.md` — proposed wire contract, delivery semantics, limitations, and controller work.

## Updated Phase 1/2 files

- `internal/agent/config.go` — bounded `MACHINARCHY_AGENT_EVENT_BUFFER_SIZE` setting.
- `internal/agent/nats.go` — bounded per-agent Core NATS subscription and explicit pending limits.
- `internal/agent/session.go` — subscribe only after identity registration and lease establishment; event processing and lease refresh integration.
- `internal/agent/config_test.go` and `internal/agent/session_test.go` — added Phase 3 validation and session tests.
- `README-INTEGRATION.md` and `ChangeLogAI.fragment.md` — updated integration instructions and change log.

## Integration

Copy `cmd/`, `internal/`, and `docs/` into the root of the Machinarchy repository, then merge `ChangeLogAI.fragment.md` into the existing root `ChangeLogAI.md`.

This bundle intentionally does not include a `go.mod`: use the repository's pinned module/dependency versions. The isolated verification used a local compile-time NATS API stub and is not a replacement for compiling and running integration tests against the repository's actual NATS dependency and broker.

## Important limitation

The event contract and NATS subject are proposed agent-side contracts. The controller's event publisher and matching shared `internal/eventing` definitions are not included. The default handler validates and records messages but does not execute operations. Do not publish production operation work until Phase 4 adds the bounded dispatcher and the controller/agent wire contracts and delivery semantics are reconciled with accepted ADRs.
