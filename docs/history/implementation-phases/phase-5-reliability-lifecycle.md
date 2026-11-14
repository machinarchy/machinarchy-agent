# Agent Phase 5 — Reliability and Lifecycle Hardening

## Scope

Phase 5 hardens the lifecycle already introduced in earlier phases. It does not add new remote execution capabilities, change the proposed wire protocol, or claim durable operation processing.

## Implemented behavior

### Coordinated process shutdown

1. The agent session stops consuming new events when its context is cancelled.
2. The event subscription is closed before worker shutdown begins.
3. The dispatcher stops accepting new work and closes its bounded queue exactly once.
4. Accepted operations are drained for up to `MACHINARCHY_AGENT_DRAIN_TIMEOUT`.
5. The dispatcher keeps its operation contexts alive independently of the session context during the graceful drain window.
6. If the drain deadline expires, outstanding operation contexts are cancelled and a fixed, additional two-second forced-cancellation grace period is observed.
7. The session returns cleanup failures to the runtime rather than logging and silently discarding them.
8. The runtime drains the broker connection after the session has completed its worker cleanup, subject to the NATS client's configured drain timeout.

A Go capability that ignores context cancellation cannot be forcibly terminated safely. The dispatcher reports a bounded shutdown error when a worker does not exit. Such capabilities must be treated as implementation defects.

### Fatal session failure

When the session exits because of an unexpected broker close, invalid control-plane response, expired/revoked lease, or another fatal condition, accepted operations are aborted rather than being allowed to continue through a potentially invalid authorization session.

Abort stops admission, cancels queued/running operation contexts, and waits only until the configured cleanup deadline. Final cancellation results are attempted while the result publisher remains available.

### Broker reconnection and event consumption

- NATS remains responsible for restoring the non-durable Core NATS subscription after reconnect.
- The session stops reading its local event channel after observing a disconnect.
- It renews the agent lease before resuming event consumption.
- If one message was selected concurrently with a disconnect, the session holds that message in memory and processes it only after connectivity and lease validity are restored.
- If lease renewal fails temporarily, event consumption remains paused until a later renewal succeeds.
- If the lease expires or renewal is permanently rejected, the session exits and aborts operations.

The NATS client may buffer subscription messages during disconnect, subject to configured pending limits. Core NATS does not replay messages published while the agent was disconnected. Slow-consumer overflow can drop messages. This is not durable event delivery.

## Shutdown configuration

`MACHINARCHY_AGENT_DRAIN_TIMEOUT` already configures the agent's bounded shutdown/drain window. No new environment variables are introduced in this phase.

## Operational semantics and limitations

- Normal SIGTERM/SIGINT shutdown drains accepted work for up to the drain timeout, followed by up to two seconds of forced-cancellation grace.
- Fatal session errors abort accepted work within the configured cleanup timeout.
- Queued operations observe cancelled contexts and report cancellation when possible.
- Running capabilities must honor their context.
- Operation queue and operation ID history remain in memory.
- Event deduplication remains a separate bounded local file ledger.
- Operation results are retried a small bounded number of times but are not written to a durable result outbox.
- A successful Core NATS flush confirms broker receipt, not controller persistence or acknowledgement.
- A process crash can still lose accepted operations or unpublished results.

Do not claim exactly-once execution, durable operation completion, or at-least-once delivery. Production-grade crash recovery requires a durable operation journal, an explicit recovery/reconciliation policy, and controller-supported durable result acknowledgement/outbox semantics.

## Verification

The bundle's isolated verification script runs formatting, tests, race-enabled tests, `go vet`, and a build against a compile-time NATS API stub. This verifies the local agent logic against the stubbed NATS surface only. It is not a substitute for running checks against Machinarchy's actual `go.mod`, the pinned NATS library, a live broker, and controller-side protocol handlers.
