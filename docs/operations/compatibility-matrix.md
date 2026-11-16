# Controller / Agent Compatibility Matrix

Maintain this table for every release. Do not assume arbitrary versions are
compatible merely because both compile.

| Controller release | Agent release | Protocol module | Status | Notes |
|---|---|---|---|---|
| Not yet verified | Hybrid execution/reliability + service operations build (unreleased) | `v0.2.3` | Not production-approved | Requires controller support for protocol v2, per-Agent JetStream consumers, durable DB outbox publication, `unknown` status, persist-before-ack result handling, shared `hostfacts/v1` ingestion, and authorization/auditing for `service.status` and `service.control` |

Update this file only after running the controller -> agent -> controller
integration suite against the tagged releases and the actual NATS ACL model.


## Platform support

Linux is the initial production target. Windows may build, but it is not
production-approved until the durable-store atomic replacement/directory-sync
behavior and process-tree cancellation are covered by native Windows tests.


The Agent's initial service backend supports Linux/systemd only. Read-only status
and mutating control are separate opt-ins; exact local service allowlists are
required. Controller authorization for these capabilities and real systemd /
polkit behavior have not yet been verified.
