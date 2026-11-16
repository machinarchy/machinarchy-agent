# Phase 3 — Host facts capability

## Scope

This slice adds a read-only `system.facts` operation to the Agent. It is
intended for controller-driven inventory refresh, not continuous telemetry.
The controller requests a snapshot when a host is enrolled, when an operator
asks to refresh facts, or on a future bounded refresh schedule.

## Facts returned

- Schema version and collection timestamp (UTC).
- Hostname, OS platform/name/version, kernel and architecture.
- Logical CPU count.
- Best-effort total/available memory, uptime and root filesystem capacity on
  Linux.
- Agent version and Go runtime version.

The capability intentionally excludes IP addresses, usernames, environment
variables, process lists, mount lists, machine IDs, hardware serial numbers and
arbitrary file contents. Platform-specific metrics that cannot be collected
are omitted rather than causing the whole operation to fail.

## Invoke

Use the existing operation request path with capability `system.facts` and an
empty JSON object as input:

```json
{}
```

The capability is read-only and is enabled by default. It does not require
`MACHINARCHY_AGENT_EXECUTION_ENABLED`.

## Compatibility

The response is versioned with `schema_version: "v1"`. Consumers must tolerate
unknown future fields and missing optional metrics. Do not treat missing metrics
as zero. The Agent's `runtime.info` capability list now includes `system.facts`.

## Rollout

1. Deploy the Agent build to a test host.
2. Confirm `runtime.info` advertises `system.facts`.
3. Request `system.facts` with `{}` and validate the result is persisted by the
   controller.
4. Verify no sensitive fields are exposed in operation output or logs.
5. Compare Linux memory and root filesystem values with local OS tools.
6. Roll out gradually and monitor operation failures and output sizes.

This Agent change does not automatically add controller inventory persistence,
refresh scheduling, or a host-details UI. Those are controller responsibilities
and must be implemented separately.


## Shared contract

The response DTOs are aliases to `github.com/machinarchy/machinarchy-protocol/hostfacts/v1`. The Agent validates the collected values against this shared contract before encoding output. The controller must use the same `hostfactsv1.Decode` function when admitting results into inventory.
