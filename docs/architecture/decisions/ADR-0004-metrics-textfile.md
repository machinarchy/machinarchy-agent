# ADR-0004: Metrics as a Prometheus Textfile

- Status: Accepted
- Date: 2026-11-19

## Context

Operators need to see whether an agent is connected, enrolled, keeping its
lease and keeping up with work, without reading logs. AGENTS.md forbids an
HTTP server without an ADR: every listener is attack surface on a managed
host, and the agent is otherwise outbound-only (it dials NATS and accepts
nothing). Three options were considered:

| Option | Exposure | Scraping |
|---|---|---|
| Prometheus textfile, read by node_exporter's textfile collector | None: no socket | node_exporter, which most fleets already run |
| HTTP on a Unix socket | Local socket, file-permission controlled | Needs a socket-aware scraper or a proxy |
| Opt-in HTTP on localhost | A TCP listener reachable by every local process | Direct, but a new listener and HTTP stack |

## Decision

The agent writes metrics to a Prometheus textfile, off unless
`MACHINARCHY_AGENT_METRICS_FILE` is set (an absolute `*.prom` path).

- The file is rewritten every `MACHINARCHY_AGENT_METRICS_INTERVAL` (default
  15s) by writing a temporary file in the same directory and renaming it, so
  a reader never sees a partial file. It is written once more when the agent
  stops, showing the final state.
- The file is mode 0644 regardless of the unit's `UMask=0077`, because the
  exporter runs as another user. It contains no secrets, identifiers of
  operations, payloads or host facts: only counts, timestamps and the agent
  version.
- Staleness is visible: `machinarchy_agent_metrics_written_timestamp_seconds`
  stops advancing when the agent is not running. node_exporter also exports
  the file's modification time.
- Warnings and errors are counted by wrapping the log handler, so "time of the
  last error" covers every failure the agent reports without instrumenting each
  call site.

## Consequences

- No listener, no new dependency, no change to the network model.
- The agent needs write access to the textfile directory. The shipped unit
  has `ProtectSystem=strict`, so operators add the directory with a drop-in
  `ReadWritePaths=` (documented in docs/deployment/systemd.md).
- Values are sampled at the interval, not at scrape time; at 15s that is well
  within normal scrape periods.
- Counters reset when the agent restarts, as with any Prometheus target.
- Hosts without node_exporter need another reader (e.g. a log shipper that
  tails the file). Revisit with an opt-in Unix-socket endpoint if that becomes
  common.
