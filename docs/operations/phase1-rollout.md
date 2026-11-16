# Phase 1 rollout gate: durable Agent operations (historical checklist)

This Agent build uses wire protocol version 2. It must not be deployed with a
protocol-v1 controller.

## Required release order

1. Review the breaking wire-protocol change in `machinarchy-protocol`.
2. Use the coordinated protocol v2 release required by the Agent build
   (currently `machinarchy-protocol` v0.2.3, including the additive
   `hostfacts/v1` and `services/v1` schemas).
3. Update the controller to use the same protocol module and accept the
   `unknown` operation status.
4. Make controller result ingestion idempotent by `operation_id`. Commit the
   result to durable storage before publishing
   `operation.result.acknowledged` to the exact Agent event subject.
5. Provision the per-Agent JetStream pull consumer and use the controller
   durable outbox as documented in `phase2-rollout.md`.
6. Run the end-to-end scenarios below against real NATS and production-equivalent
   subject ACLs.
7. Update `compatibility-matrix.md` with the verified controller, Agent and
   protocol versions. Only then consider a staged deployment.

## Required end-to-end scenarios

- Deliver a command, restart the Agent before it starts, and verify accepted
  work resumes once without duplicate execution.
- Deliver a command, restart the Agent after the `running` transition, and
  verify the controller receives `unknown`; the command must not be replayed.
- Publish a terminal result, interrupt the connection before controller
  persistence, reconnect, and verify the result is retried.
- Persist a terminal result in the controller, drop its acknowledgement, and
  verify duplicate result delivery is idempotent and acknowledged again.
- Deliver the acknowledgement and verify the Agent clears the result payload
  but retains its deduplication tombstone until event expiry.
- Saturate the Agent queue and verify the controller receives an explicit
  rejection or a retryable delivery outcome without executing rejected work.
- Revoke the Agent lease during execution and verify the session aborts work
  according to the documented cancellation semantics.

## Known limits of this Agent-side change

- The Agent binds to a pre-provisioned JetStream consumer; it does not create
  the stream or consumer. Controller provisioning and end-to-end delivery
  validation are separate deployment requirements.
- The Agent result outbox retries until the controller acknowledges; it cannot
  prove that a remote side effect happened exactly once.
- The local operation store is a bounded private JSON snapshot for a single
  Agent process. It is not replicated and does not protect against host-disk
  loss.
- The isolated test harness uses a compile-time NATS API stub. It is not a
  substitute for the end-to-end tests above.

Do not mark the compatibility matrix as production-approved until every
required scenario passes with the actual controller and broker configuration.
