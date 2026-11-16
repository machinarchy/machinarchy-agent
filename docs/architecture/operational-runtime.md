# Machinarchy Agent — Operational Runtime Architecture

**Status:** Accepted direction; implementation in progress  
**Version:** 0.2  
**Scope:** Define an original, production-oriented Agent for Machinarchy's hybrid SSH/Agent infrastructure platform.  
**Design intent:** Borrow proven ideas from Salt, Ansible, Puppet, NATS and systemd where they solve real problems; do not reproduce their feature sets or internal architectures.

> **Current Agent implementation scope (2026-11):** this document describes the
> broader long-term Machinarchy operational model. The current Agent MVP is
> intentionally narrower: `runtime.info`, `system.facts`, and opt-in
> allowlisted Linux/systemd `service.status` and `service.control`. Generic
> `exec.command` and `exec.script` are not exposed by the Agent. The shared
> `execution/v1` contract remains available for the controller's independent SSH
> transport. Package management, file deployment, users/groups, network
> configuration, watchers and desired-state resources remain future proposals,
> not current Agent capabilities.

---

## 1. Product direction

Machinarchy Agent should be a **reliable operational runtime for a managed machine**. It should make that machine discoverable, operable, configurable and observable through the Machinarchy controller.

It is not intended to be:

- a full Salt Minion clone;
- a general-purpose plugin host;
- a second controller running on every machine;
- a shell wrapper that happens to communicate over NATS;
- a requirement for every managed host.

Machinarchy supports two independent execution paths:

- **SSH executor:** controller-initiated, useful for simple, agentless and occasional management;
- **Agent executor:** host-initiated persistent connection, useful for reliable ongoing management, events, local facts and richer capabilities.

Both must fit the same inventory, operation and result model. A host may have SSH, Agent, both or neither. The controller chooses a supported transport per host according to policy. The Agent must never assume that SSH exists, and SSH-only hosts must remain useful without installing an Agent.

### Current starting point

The current Agent package provides the runtime/session foundation, durable
operation lifecycle, host facts, and constrained Linux/systemd service
operations. Generic command and script execution are intentionally excluded
from the current Agent profile. The shared `execution/v1` contract remains a
separate transport-neutral contract for controller-side SSH; it does not imply
that the Agent exposes shell execution.

The first reliability implementation adds a bounded local operation store, crash recovery rules, and a result outbox that retries until the controller acknowledges durable persistence. Inbound commands now bind to a controller-provisioned JetStream durable pull consumer. The controller-side overlay provides stream/consumer provisioning, DB-outbox publishing and persist-before-ack result handling contracts, but it has not yet been integrated with the actual controller database/broker packages or verified end-to-end. A command runner that works in a happy-path test is not yet a reliable fleet-management runtime.

### Product promise

> Tell Machinarchy what outcome you need, see what it will do, execute it on the right machines, and understand exactly what happened.

This promise is more important than the number of built-in modules.

## 2. Design principles

1. **Outcome before command.** Prefer typed, structured operations such as `service.ensure` over shell snippets for common administration.
2. **Keep the Agent surface narrow.** Generic shell and script execution are not part of the current Agent MVP; the controller may operate its independent SSH transport under separate policy.
3. **Predictable behavior.** The same request has the same meaning across SSH and Agent, or fails clearly when a transport cannot support it.
4. **Safe by default.** Least privilege, bounded resources, explicit destructive behavior and no implicit privilege escalation.
5. **Durability before cleverness.** Persist accepted work and pending results; do not rely on in-memory state for production correctness.
6. **No false exactly-once claims.** Delivery may be repeated. Operation identity and a local ledger prevent avoidable duplicate starts, but arbitrary side effects cannot be made exactly-once by a message broker.
7. **Small trusted core.** Capabilities are versioned and explicitly shipped. Do not download and execute arbitrary modules from the controller.
8. **Useful offline behavior.** Reconnect safely and report status. Do not execute stale commands merely because they were queued during an outage.
9. **Explainability is a feature.** Every action should expose why it was selected, what it will change, what permission it needs and how it ended.
10. **Incremental complexity.** Build remote execution first, structured capabilities second, desired-state behavior after the resource contracts are proven.

## 3. What Machinarchy should learn from other systems

| Reference | Useful idea | Machinarchy adaptation | What not to copy |
|---|---|---|---|
| Salt Grains | Machine facts used for discovery and targeting | Versioned, timestamped `facts` with clear freshness and source | A large dynamically loaded grain framework |
| Salt execution modules | Reusable operations on managed machines | Small, typed capability catalog with input/output schemas and permissions | Hundreds of loosely standardized functions |
| Salt Beacons | Local events from machine conditions | A few reliable watchers with deduplication, rate limits and event history | Unbounded event plugins or automatic reactions by default |
| Salt Pillar | Targeted configuration data | Versioned, host-scoped configuration assignments and secret references | A general-purpose templating/rendering system in the Agent |
| Ansible inventory and ad-hoc execution | Simple targeting and one-off work | One inventory and one hybrid execution interface | Reimplementing all inventory plugins and playbook syntax |
| Ansible check/diff modes | Preview changes before applying | Capability-specific `check` and safe diff with explicit support declarations | Pretending arbitrary shell commands can be dry-run safely |
| Puppet resources/catalogs | Declarative desired state and reports | Typed resources with `inspect`, `plan`, `apply` and structured reports | A large bespoke DSL and catalog compiler in the first release |
| NATS JetStream | Persistent streams and acknowledged delivery | Durable command delivery, explicit acknowledgements and replay where configured | Treating broker acknowledgement as proof that a command ran exactly once |
| systemd | Service supervision and OS-level sandboxing | A hardened service unit, restart policy, resource limits and filesystem restrictions | Reimplementing OS process supervision inside the Agent |

The key synthesis is: **Salt's operational breadth, Ansible's approachable workflow, Puppet's resource correctness, and NATS/systemd's reliability primitives — with a much smaller supported surface.**

## 4. Agent responsibilities and boundaries

### 4.1 Agent responsibilities

The Agent owns local execution and local truth:

- its cryptographic identity and credentials;
- persistent outbound connection and session lifecycle;
- machine facts and capability advertisement;
- validation and local policy enforcement for received operations;
- process and capability execution;
- bounded job state and result persistence;
- local health reporting;
- selected local watchers;
- applying approved configuration/resource instructions;
- safe shutdown and recovery.

### 4.2 Controller responsibilities

The controller remains the authority for fleet-wide decisions:

- inventory, workspaces, groups, labels and targeting;
- user authentication, RBAC and audit identity;
- Agent enrollment, association, revocation and credential rotation;
- choosing SSH or Agent per host;
- scheduling, job creation and fleet concurrency;
- desired configuration assignments and versioning;
- policy distribution and approval workflows;
- global job history, event history and result aggregation;
- event-to-action automation and cross-host orchestration.

### 4.3 Things the Agent must not decide independently

The Agent must not invent its own target set, choose a different host, broaden a user's authorization, select an alternate transport, or turn a monitoring event into a privileged shell command unless an explicit controller policy authorizes that behavior.

The Agent may enforce stricter local safety policy than the controller requested. It must never weaken local policy to satisfy a controller request.

## 5. The operator experience

The Agent is a background service, not a product users should have to babysit. Most day-to-day interaction should happen through the Machinarchy controller CLI/API. The local Agent CLI is primarily for installation, diagnosis and recovery.

### 5.1 The normal workflow

```bash
# Inspect what Machinarchy knows about a machine.
machinarchy host inspect web-01

# Preview host selection and transport choices; no command is sent.
machinarchy exec --target 'role=web,env=production' --plan -- uptime

# Run a simple command across the target set.
machinarchy exec --target 'role=web,env=production' -- uptime

# Use a structured service capability instead of shell where possible.
machinarchy service ensure nginx --state running --enabled

# Follow one job and inspect a failed host.
machinarchy job watch job-8f31
machinarchy job inspect job-8f31 --host web-02
```

Exact CLI syntax is a separate CLI design decision. The important UX is that the user can see the resolved hosts, chosen transport, capability compatibility and policy before execution.

### 5.2 Local Agent diagnostics

The local binary should offer a small, memorable diagnostic surface:

```bash
machinarchy-agent version
machinarchy-agent status
machinarchy-agent doctor
machinarchy-agent config validate
machinarchy-agent capabilities list
machinarchy-agent test-connection
```

`doctor` should be a high-value command, not a generic dump. It should check:

- configuration file exists, parses and has safe permissions;
- identity exists, is readable only by the service identity and is not revoked;
- controller/broker DNS and TLS connectivity;
- credential validity and authorization failures;
- clock skew, because expiry and replay protections depend on time;
- local state database integrity and free disk space;
- service account and effective permissions;
- execution backend availability;
- disk space for spool/state directories;
- version/protocol compatibility.

For each failed check, show **symptom → likely cause → suggested next command**. Do not print private keys, tokens, secret values or full environment dumps.

### 5.3 Preview and explainability

A job plan should answer:

- Which hosts matched?
- Which hosts were excluded and why?
- Which transport will be used for each host?
- Does that transport support every requested feature?
- What privilege level is required?
- Is the operation read-only, mutating or unknown-risk?
- Is a real `check` implementation available, or is this only a dispatch preview?
- What are the concurrency and timeout limits?

Do not call a targeting preview a dry run. Do not call an arbitrary shell command safe to simulate. Distinguish `--plan` (controller-side planning) from `--check` (a capability-specific remote inspection that promises not to mutate state).

## 6. Capability model: the core product abstraction

A capability is a supported, versioned operation that a managed machine knows how to perform.

Examples:

- `system.facts`
- `system.health`
- `process.inspect`
- `service.status` (implemented first cut; Linux/systemd, local allowlist)
- `service.control` (implemented first cut; start/stop/restart only, separately enabled)
- `service.inspect` (future normalized cross-provider inspection)
- `service.ensure` (future idempotent desired-state reconciliation)
- `package.inspect`
- `package.ensure`
- `file.inspect`
- `file.ensure`
- `network.check`
- `exec.command`
- `exec.script`

Names above are proposals; establish naming conventions before freezing the public protocol.

### 6.1 Why capabilities are better than a large module loader

Every capability has a discoverable contract. It can be documented, tested, authorized, versioned and checked for transport compatibility. Users do not need to know implementation details such as `systemd`, `apt`, `dnf` or NATS subjects to use the common operation.

A capability should not be added merely because a command can be wrapped. It should be added when it provides at least one of these benefits:

- structured input validation;
- consistent output across supported operating systems;
- idempotency or desired-state semantics;
- a real check/preview mode;
- better authorization or auditing;
- safer behavior than a hand-written shell command;
- reduced operational toil.

Otherwise, defer the feature rather than adding a generic shell wrapper to the Agent.

### 6.2 Capability descriptor

Each Agent capability should publish a descriptor containing:

```yaml
name: service.ensure
version: 1
summary: Ensure a service has the requested state
input_schema: service.ensure.v1
output_schema: resource.result.v1
platforms:
  - linux
providers:
  - systemd
risk: privileged_mutation
idempotent: true
supports:
  check: true
  diff: false
  cancellation: best_effort
requirements:
  privilege: service_control
limits:
  default_timeout: 30s
  max_timeout: 2m
```

The descriptor is metadata, not an authorization grant. The controller uses it for planning and UX; the Agent still validates requests and enforces local policy.

Capabilities must report provider and version details. If a host does not support a capability, Machinarchy should return a structured `unsupported` result with a reason, not attempt an arbitrary fallback command.

### 6.3 Capability contract

For structured resource capabilities, standardize these conceptual operations:

1. `Inspect` — report current state without mutation.
2. `Check` — compare current and requested state and describe proposed changes without applying them, where supported.
3. `Apply` — make the resource match the requested state.
4. `Verify` — confirm the postcondition after apply.

Not every capability needs all four operations. The descriptor must say which are supported. `Apply` should return a structured result containing `changed`, `before`, `after`, warnings, provider and any error.

A capability that claims to be idempotent must prove it in tests: applying the same desired state twice should produce no second change.

## 7. Recommended capability set

Build a small set of high-value operations. Avoid feature parity for its own sake.

### 7.1 Foundation — first production milestone

| Capability | Value | Initial behavior |
|---|---|---|
| `system.facts` | Understand and target machines | OS, distro, architecture, kernel, hostname, CPU/memory summary, boot time and agent version |
| `system.health` | Know whether the Agent is healthy | Agent process health, state DB, broker session, last successful job, disk usage |
| `process.inspect` | Diagnose workloads | List/find process metadata; do not expose arbitrary process memory or secrets |
| `exec.command` | Flexible ad-hoc administration | Program + argument vector; no implicit shell parsing |
| `exec.script` | Existing operational escape hatch | Explicit shell/interpreter policy, bounded script input and output |
| `agent.diagnostics` | Reduce support effort | Structured local checks and reason codes |

Facts should be lightweight and collected at startup plus a controlled refresh interval. Avoid uploading every installed package, every process, full environment variables, or sensitive filesystem data during each heartbeat.

### 7.2 System operations — next useful group

| Capability | Value | Design constraint |
|---|---|---|
| `service.status` | Initial read-only systemd unit inspection | Implemented on Linux; exact local unit allowlist; normalized status contract |
| `service.control` | Initial start/stop/restart actions | Implemented on Linux; separate mutation opt-in; no arbitrary flags or privilege escalation |
| `service.inspect` | Future cross-provider status and startup-policy inspection | Provider-aware; report unsupported service manager explicitly |
| `service.ensure` | Future desired-state reconciliation | Idempotent; check mode before mutation; may later manage running/enabled state |
| `package.inspect` | Check installed version | Read-only where possible |
| `package.ensure` | Ensure package present/absent or requested version | Explicit package provider; lock contention and reboot implications reported |
| `file.inspect` | Inspect metadata, checksum and ownership | Avoid reading secret file contents by default |
| `file.ensure` | Ensure file/directory exists or has expected content/mode | Atomic write, permission handling, backup/rollback rules, secret-aware diff suppression |
| `network.check` | Diagnose DNS/TCP/HTTP reachability | Explicit target/port; bounded duration; no unrestricted port scanning |

Do not implement all operating-system providers at once. Start with the Linux distributions and service/package managers actually used in the project's test environments. Publish a support matrix.

### 7.3 Configuration and artifacts — after basic operations are reliable

Provide a small, versioned configuration assignment model:

- controller creates a versioned assignment;
- assignment targets hosts using the controller's inventory model;
- Agent receives only its own assignment;
- Agent validates schema and local constraints;
- Agent stages the new configuration;
- Agent applies atomically where possible;
- Agent reports applied version and result;
- last-known-good configuration remains available for rollback.

Artifact delivery should use immutable, content-addressed artifacts with size limits and checksum/signature verification. Do not turn the Agent into a general file server or accept arbitrary URLs and download destinations from untrusted job payloads.

### 7.4 Monitoring and events — later

Start with a few watchers that solve common operational problems:

- disk usage threshold;
- service availability/state change;
- Agent session/runtime health;
- optionally selected process health.

A watcher must have a clear interval or event source, threshold, severity, deduplication key, rate limit and event schema. Publish transitions (for example, `healthy → degraded`) rather than sending the same alert every poll.

Monitoring must not automatically execute shell commands. The controller may later support explicit, audited event-to-action policies with approval, cooldown, maximum attempts and loop prevention.

### 7.5 Desired-state engine — later, but design for it now

The desired-state system should be built from typed resource capabilities, not from a free-form scripting language.

Example:

```yaml
apiVersion: machinarchy.io/v1
kind: HostPolicy
metadata:
  name: web-baseline
spec:
  resources:
    - type: package
      name: nginx
      state: present
    - type: service
      name: nginx
      state: running
      enabled: true
```

A future `machinarchy apply --check` should show which resources are compliant, which would change and which cannot be evaluated. A real apply should report changed/unchanged/failed per resource. Resources must be independently idempotent and ordered through explicit dependencies, not hidden shell sequencing.

Do not implement a full state DSL, template language or dependency resolver until the first resource types and their check/apply semantics are stable.

## 8. Reliability architecture

Reliability is not just reconnect logic. It is the combination of durable acceptance, operation identity, resource bounds, process supervision and truthful result states.

### 8.1 Agent local durable state

Keep local persistence limited to state the Agent needs to recover its own work:

- accepted operation IDs and their lifecycle state;
- operation start/finish timestamps and result references;
- pending terminal results awaiting controller acknowledgement;
- compact deduplication tombstones.

The initial implementation uses a bounded, private JSON snapshot behind the `OperationStore` interface. It is atomically replaced, synchronized to disk, restricted to one Agent process per state directory, and bounded by record and pending-result limits. This deliberately avoids introducing a database before the Agent has demonstrated the need. If measured write amplification, history requirements or concurrency justify transactional storage, add a SQLite implementation behind the same interface with an ADR. Do not store long-lived secrets or unbounded stdout/stderr in the operation store.

### 8.2 Operation lifecycle

Use an explicit state machine. Suggested states:

```text
received
  -> validated
  -> accepted (durably recorded)
  -> running
  -> succeeded | failed | cancelled | expired | unknown
```

Invalid, unauthorized, expired or unsupported requests should be rejected before acceptance with stable error codes.

The Agent should acknowledge a durable message only after it has validated and durably recorded the operation. A repeated delivery with the same operation ID should return the known state/result and must not start a second local process.

### 8.3 Delivery semantics

NATS Core publication alone does not provide durable offline delivery or replay. For production operation delivery, use a durable messaging path such as NATS JetStream or an equivalent persistent controller outbox/queue design.

Even with JetStream, the system should assume **at-least-once delivery**. The Agent must persist the operation ID before acknowledging the command, and the controller must deduplicate result messages. This reduces duplicate execution but does not make arbitrary external side effects exactly-once.

Result handling should be:

1. Agent finishes an operation.
2. Agent commits terminal state and result metadata to local storage.
3. Agent publishes result from its durable outbox.
4. Controller persists the result and acknowledges/records it.
5. Agent marks the result delivered and prunes it according to retention policy.

If the connection is lost, the result remains pending and is published after reconnect.

### 8.4 Reconnect and stale work

- Exponential backoff with jitter and maximum delay.
- Heartbeats with jitter to avoid synchronized fleet bursts.
- Distinguish connected, authenticated, subscribed and ready states.
- Resume durable consumption from the last confirmed point.
- Reject commands with expired `expires_at`, wrong Agent identity or incompatible protocol version.
- Do not execute queued work after its deadline.
- Do not queue dangerous operations indefinitely while offline.
- Expose last contact and last successful operation separately.

### 8.5 Process execution controls

All process execution must have:

- bounded worker count and queue depth;
- per-operation deadline and maximum output bytes;
- separate stdout/stderr capture and truncation metadata;
- controlled working directory and environment;
- process-tree termination on supported operating systems;
- child process reaping and file descriptor cleanup;
- cancellation state that distinguishes requested from confirmed termination;
- explicit executable resolution rules;
- bounded stdin and script sizes;
- structured errors rather than raw string parsing.

Do not launch a new process for every polling operation if a safe native API or system provider exists.

### 8.6 Result states must be truthful

`failed` means the Agent knows the requested execution completed unsuccessfully. `unknown` means execution may have occurred but the Agent cannot prove the outcome. `cancelled` should mean termination was confirmed; otherwise expose `cancellation_requested` or `unknown`.

The controller must never treat a connection timeout after dispatch as proof that the command did not run.

## 9. Security and privilege model

The Agent is a high-value machine credential. A compromised Agent or overly powerful controller permission can become fleet-wide compromise.

### 9.1 Network identity

- Unique identity per Agent; no fleet-wide shared credential.
- Encrypted transport with server certificate verification.
- Narrow broker subject permissions scoped to that Agent.
- Credential rotation and immediate revocation.
- Enrollment token is short-lived, single-use and bound to a Host/workspace.
- Commands include target Agent identity, operation ID, issue time, expiry and protocol version.
- Audit the controller principal that authorized every operation.

### 9.2 Least privilege: separate network control from privileged work

Do not solve package/service management by silently running the whole network-facing Agent as root.

Recommended long-term Linux model:

```text
Controller / NATS
       |
       v
machinarchy-agent (unprivileged)
  - identity and network connection
  - validates operations
  - local ledger and outbox
  - unprivileged command execution
       |
       | narrow local IPC (Unix socket)
       v
machinarchy-execd (privileged helper, optional)
  - fixed allowlisted privileged capabilities
  - strict input schema and path validation
  - no arbitrary shell or arbitrary executable path
```

The privileged helper should be a deliberately small trusted component. It should accept only typed operations such as approved service control, package operations or file changes within explicitly permitted paths. It should verify peer credentials, validate every field, apply rate/time/resource limits and emit auditable results.

Generic command and script execution are outside the current Agent scope. Any future proposal to add them must be reviewed as a separate high-risk capability and must not silently change the Agent privilege model.

This privilege-separated design is a target architecture. It should be implemented when privileged capabilities are introduced; do not build a broad generic root RPC interface.

### 9.3 Secrets

- Never include secrets in facts, ordinary results, logs or capability descriptors.
- Do not expose the Agent's broker credentials to child processes.
- Prefer short-lived, operation-scoped secret injection.
- Avoid secrets in command-line arguments and process environment where possible.
- Use protected temporary files or file descriptors when a target application requires them, and clean them up.
- Suppress file diffs and output likely to contain secret material.
- Keep local secret persistence minimal and document the threat model.

### 9.4 Sandboxing

Use systemd hardening as a baseline where available: dedicated service user, restricted filesystem access, `NoNewPrivileges`, private temporary directory, address-family and syscall restrictions where compatible, resource limits and a controlled writable state directory. Treat sandbox settings as defense in depth, not as a substitute for application-level authorization.

### 9.5 Extensibility

Do not load unsigned code or scripts as new capability implementations from the controller. Initially:

- capabilities are compiled into the Agent;
- capability metadata is versioned;
- optional external extensions, if ever introduced, must be signed, version-pinned, sandboxed and disabled by default.

A downloaded shell script is still code execution, not a harmless configuration update.

## 10. Protocol and compatibility

The Agent and controller are released independently, so compatibility must be explicit.

Every Agent session should advertise:

- Agent version;
- protocol major/minor version;
- supported capability names and versions;
- platform/provider information;
- execution policy summary (without secrets);
- last facts refresh time.

Rules:

1. Unknown protocol major version: reject connection or enter a clearly degraded compatibility mode.
2. Unknown optional capability: controller must not dispatch it.
3. Unknown input field: reject where strict validation is required; protocol evolution must be deliberate.
4. Capability version mismatch: return a structured compatibility error.
5. Never silently reinterpret a request under a newer schema.
6. Maintain a controller/Agent compatibility matrix and test supported version combinations in CI.

Keep the shared protocol transport-neutral. Do not put NATS-specific details in command/script/resource request types.

## 11. SSH/Agent parity

The controller should plan each operation against a per-transport capability matrix.

| Operation | SSH | Agent | Initial expectation |
|---|---|---|---|
| Basic command | Yes | Yes | Same semantic request/result |
| Script | Yes | Yes | Explicit interpreter and input handling |
| System facts | Best-effort collection | Native structured facts | Same normalized subset; Agent may provide richer freshness |
| Service ensure | Provider/script implementation | Native capability/provider | Same postcondition, support declared per host |
| Package ensure | Provider/script implementation | Native capability/provider | Same postcondition, explicit provider support |
| File ensure | SFTP/SSH implementation | Native capability | Same resource semantics where possible |
| Continuous watchers | No persistent channel by default | Yes | Agent-only initially; clearly shown in UI |
| Durable local job recovery | Limited by SSH session | Yes | Agent advantage |

Parity does not mean identical internal implementation. It means the same public request has documented semantics and the planner knows whether each transport can honor it.

Transport fallback is safe only before the remote side can have accepted/started work. If the first attempt has an uncertain outcome, mark it `unknown`; do not automatically run the operation through the other transport.

## 12. Recommended repository boundaries

Keep this modular and incremental. Do not split every concept into a package just for appearance.

```text
machinarchy-agent/
├── cmd/machinarchy-agent/
├── internal/
│   └── agent/
│       ├── runtime/       # lifecycle, readiness, shutdown
│       ├── identity/      # local identity and credentials
│       ├── session/       # broker connection, heartbeat, lease
│       ├── protocol/      # envelope validation and compatibility
│       ├── operations/    # admission, lifecycle and cancellation
│       ├── ledger/        # durable local operation state
│       ├── outbox/        # durable result/event publishing
│       ├── capabilities/  # registry and built-in capabilities
│       │   ├── system/
│       │   ├── process/
│       │   ├── service/
│       │   ├── package/
│       │   └── file/
│       ├── facts/         # machine facts and freshness
│       ├── watchers/      # bounded event producers
│       ├── config/        # validated, versioned local config
│       └── privilege/     # IPC client for optional privileged helper
├── internal/platform/
│   ├── linux/
│   └── windows/           # only when process and service semantics are supported
├── deployments/
├── docs/
└── tests/
```

This is a target boundary, not a requirement to move all current files immediately. Preserve existing code and refactor only when a package has a real ownership or testing problem. Keep transport/broker code out of capability implementations; keep capability implementations out of NATS handlers.

The privileged helper, if implemented, may live in a separate executable/module so its dependency surface remains small.

## 13. Production support policy

Do not claim “cross-platform” merely because the Go binary compiles on multiple operating systems.

For the first production target, recommend:

- Linux x86_64 and arm64;
- explicitly tested distributions and service managers;
- systemd as the first supported service supervisor;
- explicit supported package providers;
- tested process-tree cancellation;
- documented container deployment constraints.

Windows and macOS should be added only after their process-tree cancellation, service integration, privilege boundaries, installers, filesystem behavior and CI coverage are real. SSH can continue to manage hosts for which the Agent is not yet supported.

## 14. Implementation roadmap

### Phase 0 — Architecture and contracts

- Approve Agent responsibilities and controller/Agent boundary.
- Define capability descriptor and versioning rules.
- Define operation lifecycle and stable error codes.
- Define fact schema and freshness rules.
- Define Agent/SSH capability parity rules.
- Define local durable ledger and result outbox interfaces.
- Record decisions as ADRs before large refactoring.

**Exit criteria:** request/result semantics are documented; no capability can be dispatched without a declared contract; compatibility policy is testable.

### Phase 1 — Reliable Agent runtime (in progress)

Implemented in the first Agent reliability slice:

- Bounded local operation store with atomic snapshot replacement and restrictive file permissions.
- Durable acceptance before enqueue and `running` persistence before capability invocation.
- Restart recovery: resume only accepted work; convert interrupted running work to `unknown`.
- Result outbox with retry until controller acknowledgement; strip raw request/event payloads once execution starts, and retain a compact deduplication digest after acknowledgement.
- Bounded pending operations/results and backpressure.
- Startup guard for a recovered backlog larger than the configured queue capacity.
- Regression tests for duplicate operation IDs, crash recovery, late cancellation and orderly outbox shutdown.

Still required before this phase can be called production-ready:

- Controller result persistence and acknowledgement integration.
- Durable inbound command delivery (for example JetStream) with correct acknowledgement timing.
- End-to-end reconnect, disconnect and crash testing against real NATS and the controller.
- Production OS process-tree cancellation support and tested deployment/storage behavior.
- Explicit supported controller/Agent compatibility matrix.

**Exit criteria:** kill/restart and network interruption tests do not lose accepted results or start duplicate local operations; uncertain outcomes are represented honestly; and the controller can prove durable receipt before acknowledging results.

### Phase 2 — Machine awareness and usability

- `system.facts` with versioned schema and collection timestamps.
- `system.health` and Agent self-metrics.
- `machinarchy-agent doctor`.
- Capability listing and compatibility reporting.
- Clear structured errors and remediation hints.
- Local log fields correlated by Agent ID and operation ID.

**Exit criteria:** an operator can diagnose most installation, identity, broker, permission and disk/state failures without reading source code.

### Phase 3 — Core operational capabilities

- `process.inspect`;
- `service.inspect` and `service.ensure`;
- `package.inspect` and a small set of `package.ensure` providers;
- structured check/apply/verify results;
- privilege-separated helper for narrowly scoped privileged operations.

**Exit criteria:** supported operations are idempotent, documented, permission-aware and tested on every claimed platform/provider combination.

### Phase 4 — Configuration and artifact delivery

- Versioned host configuration assignments;
- schema validation and last-known-good rollback;
- immutable artifacts with checksums/signatures;
- atomic file operations and secret-safe output handling.

**Exit criteria:** interrupted configuration changes recover safely and report the applied version.

### Phase 5 — Watchers and events

- disk, service and Agent-health watchers;
- deduplication and rate limiting;
- event schema and retention policy;
- controller-side event history and explicit automation policies.

**Exit criteria:** no event storm under repeated failures; watchers do not trigger privileged actions on their own.

### Phase 6 — Desired-state workflows

- resource declarations;
- dependency ordering;
- plan/check mode;
- apply and verify;
- drift reporting and controlled remediation.

**Exit criteria:** resources are idempotent, plans are truthful, and partial failure/retry behavior is documented.

### Phase 7 — Distribution and controlled updates

- signed release manifest and artifacts;
- platform/architecture selection;
- staged upgrade and rollback;
- controller-coordinated rollout rings;
- compatibility guardrails and upgrade telemetry.

**Exit criteria:** interrupted upgrades recover to a known runnable version and fleet rollouts can be paused/rolled back.

## 15. Required reliability and security test scenarios

A production-ready claim should require automated tests for at least:

### Delivery and crash recovery

- broker disconnect before delivery;
- disconnect after delivery but before local acceptance;
- crash after durable acceptance but before ACK;
- redelivery of the same operation ID;
- crash during process execution;
- crash after process completion but before result publish;
- result publish succeeds but acknowledgement is lost;
- expired queued operation after a long outage;
- disk full or local state corruption;
- duplicate and out-of-order lifecycle events.

### Process safety

- timeout and cancellation;
- child/descendant process cleanup;
- very large stdout and stderr;
- invalid UTF-8 output;
- process exits non-zero while preserving output;
- invalid working directory;
- environment injection attempts;
- output containing known secret markers;
- concurrency and queue saturation;
- repeated cancel requests.

### Security

- revoked Agent credential;
- wrong Agent ID in operation envelope;
- expired or replayed request;
- unauthorized capability;
- path traversal and symlink attacks for file operations;
- untrusted executable path;
- attempt to use privileged helper for arbitrary shell;
- secret leakage through logs, facts, environment or result;
- compromised or unavailable broker behavior.

### Compatibility and platform

- current and previous supported controller version;
- current and previous supported Agent version;
- unsupported capability version;
- unsupported OS/provider;
- Linux process-group cancellation;
- systemd sandbox restrictions;
- container state persistence and filesystem permissions.

## 16. Explicit non-goals for now

Do not implement these just because Salt has them:

- Salt-compatible module/state/pillar syntax;
- a generic module loader or runtime code download;
- a broad templating language;
- Salt Mine or a distributed arbitrary key/value cache;
- multi-master, syndic or proxy minions;
- automatic event-to-shell reactors;
- a full remote terminal system;
- package repository mirroring;
- every package manager and service manager;
- a local scheduler that competes with the controller scheduler;
- self-updating Agent before signed rollout and rollback are designed.

If a feature does not materially improve safety, usability, operability or reliability, leave it out.

## 17. Current architecture decisions

The following decisions have been accepted as the starting direction; revisit them only with evidence and an ADR where appropriate:

| Decision | Recommendation |
|---|---|
| Agent role | Local operational runtime; controller remains fleet authority |
| Production OS scope | Linux first, explicit tested support matrix |
| Execution foundation | Typed OS capabilities in the Agent; controller SSH remains a separate executor |
| Arbitrary shell privilege | Agent service identity by default; never implicit root |
| Privileged actions | Narrow local privileged helper, not a generic root shell |
| Durable operation state | Bounded private atomic snapshot behind `OperationStore`; add SQLite only when measurements justify it |
| Message delivery | Durable delivery path (for example JetStream) plus local ledger/outbox |
| Delivery guarantee | At least once; deduplicate by operation ID; no exactly-once claim |
| Offline commands | Expire by default; no indefinite stale queue |
| Dynamic plugins | Excluded initially; built-in versioned capabilities |
| Facts | Small, timestamped, non-sensitive and refreshable |
| Monitoring | Few rate-limited watchers; no autonomous remediation by default |
| Desired state | Typed resources with inspect/check/apply/verify, introduced after runtime reliability |
| Transport parity | Same semantics where declared supported, not forced identical implementation |

## 18. Inspiration and references

These are design references, not compatibility requirements:

- Salt overview and execution architecture: https://docs.saltproject.io/salt/user-guide/en/latest/topics/overview.html and https://docs.saltproject.io/salt/user-guide/en/latest/topics/execution-architecture.html
- Salt modular systems and execution modules: https://docs.saltproject.io/en/latest/topics/development/modules/index.html
- Salt system architecture, Beacons and Reactor: https://docs.saltproject.io/en/3006/topics/salt_system_architecture.html
- Salt scheduling: https://docs.saltproject.io/salt/user-guide/en/latest/topics/scheduler.html
- Ansible check and diff modes: https://docs.ansible.com/projects/ansible/latest/playbook_guide/playbooks_checkmode.html
- Puppet agent/server architecture and catalog application: https://help.puppet.com/core/current/Content/PuppetCore/architecture.htm
- NATS JetStream persistence and at-least-once delivery: https://docs.nats.io/concepts/jetstream
- systemd execution sandboxing: https://www.freedesktop.org/software/systemd/man/latest/systemd.exec
- OpenTelemetry semantic conventions: https://opentelemetry.io/docs/concepts/semantic-conventions/

---

## Final recommendation

Do not measure Machinarchy Agent by how many Salt Minion features it has. Measure it by whether an operator can safely and confidently manage a real fleet with fewer surprises.

The correct first investment is not another hundred capabilities. It is a durable execution lifecycle, a useful machine-facts model, a good diagnostics command, a small typed capability catalog, explicit privilege boundaries and honest results.

Once those foundations are reliable, service/package/file operations, configuration delivery, watchers and desired-state management can be added without turning the Agent into an unmaintainable framework.
