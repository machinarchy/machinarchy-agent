# Controller / Agent Compatibility Matrix

Maintain this table for every release. Do not assume arbitrary versions are
compatible merely because both compile.

| Controller release | Agent release | Protocol module | Status | Notes |
|---|---|---|---|---|
| Not yet verified | Agent OS-management MVP (unreleased) | `protocol/v0.1.0` (planned first nested-module tag; wire v2) | Not production-approved | Requires controller support for protocol v2, per-Agent JetStream consumers, durable DB outbox publication, `unknown` status, persist-before-ack result handling, shared `hostfacts/v1` ingestion, and authorization/auditing for `service.status` and `service.control` |

Update this file only after running the controller -> agent -> controller
integration suite against the tagged releases and the actual NATS ACL model.


## Platform support

| OS | Architecture | Deployment | Status |
|---|---|---|---|
| Linux with systemd | AMD64 | Native binary | Release packaging target; live integration still required |
| Linux with systemd | ARM64 | Native binary | Release packaging target; live integration still required |
| Linux without systemd | AMD64/ARM64 | Native binary | Not supported by the systemd installer |
| Docker Engine | AMD64 | Container | Host-facts-only profile; optional systemd D-Bus/Polkit integration pending live host validation |
| Docker Engine | ARM64 | Container | Host-facts-only profile; optional systemd D-Bus/Polkit integration pending live host validation |
| Windows/macOS | Any | Native binary | Not production-supported |

Windows may build, but it is not production-approved until the durable-store
atomic replacement/directory-sync behavior and process-tree cancellation are
covered by native Windows tests.


The Agent's initial service backend supports Linux/systemd only. Read-only status
and mutating control are separate opt-ins; exact local service allowlists are
required. Docker service integration additionally requires the optional host
D-Bus Compose overlay and a host Polkit rule. The initial Docker deployment
supports rootful Docker with default UID/GID mapping; rootless Docker and
`userns-remap` are not yet supported. The design is statically checked, but real
Docker, D-Bus and Polkit behavior must still be verified on clean AMD64 and
ARM64 Linux hosts before production approval.
