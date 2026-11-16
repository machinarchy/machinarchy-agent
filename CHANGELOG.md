# Changelog

User-visible changes are documented here. Internal AI-assisted implementation
notes and file-level details belong in `ChangeLogAI.md`.

## Unreleased

- Added opt-in Linux/systemd `service.status` and `service.control` Agent
  capabilities with exact local unit allowlisting and separate control opt-in.
- Added bounded systemd status parsing, fixed-argv lifecycle actions, context
  cancellation, and a minimal child environment that excludes Agent secrets.
- Added shared `services/v1` contracts (protocol module v0.2.3).

- Moved the `system.facts` DTOs into the shared `machinarchy-protocol/hostfacts/v1`
  module and validate collected facts against that contract before publishing.

- Added the read-only `system.facts` capability for host inventory refresh.
  It reports a versioned, bounded snapshot of hostname, OS/kernel, architecture,
  logical CPU count and best-effort Linux memory, uptime and root-filesystem
  metrics. Sensitive details such as IP addresses, usernames, environment
  variables, process lists and machine IDs are intentionally excluded.

- Replaced inbound Core NATS event subscription with a controller-provisioned
  JetStream durable pull consumer using explicit acknowledgement and delayed
  retry for transient local failures.
- Added bounded JetStream stream/consumer binding configuration and Agent-side
  validation that the pre-provisioned consumer is scoped to the assigned Agent.
- Made queue-full terminal rejections durable in the local result outbox so
  the controller can acknowledge every terminal result consistently.
- Added controller integration overlay for JetStream stream/consumer
  provisioning, stable-ID command outbox publication and persist-before-ack
  result processing.

- Added a bounded durable Agent operation store. Accepted work is persisted
  before enqueue, `running` is persisted before execution, and interrupted
  running operations recover as `unknown` rather than being replayed.
- Added a durable result outbox with retry-until-controller-acknowledgement
  behavior and bounded backpressure when results remain unacknowledged.
- Added the Agent protocol v2 result acknowledgement event and `unknown`
  operation status. Controller support is required before deployment.

- Removed Agent-side generic command and shell-script execution from the
  minimal OS-management profile. The shared execution contract remains available
  for the controller's independent SSH transport.

- Extracted the agent runtime into an independent Go module.
- Added standalone build, test, container and systemd deployment scaffolding.
- Moved versioned wire contracts to the separate `machinarchy-protocol` module.
