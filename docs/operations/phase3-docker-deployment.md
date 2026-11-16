# Phase 3 — Docker Engine Deployment and Host Integration

## Scope

This phase adds Linux Docker Engine deployment only. Kubernetes, privileged
containers, Docker socket access, host PID/network namespaces, and a host-root
filesystem mount are explicitly out of scope.

## Implemented

- Added the `systemctl` client to the runtime image. The container does not run
  the systemd daemon; it contacts the host system manager through the host
  system D-Bus socket only when the optional Compose overlay is enabled.
- Added a default host-facts-only `compose.yaml` profile. It runs as UID/GID
  10001, uses a persistent state volume, read-only root filesystem, temporary
  `/tmp`, dropped Linux capabilities, and `no-new-privileges`.
- Shares the host UTS namespace to report the host hostname and mounts only the
  host `/etc/os-release` file read-only at `/run/machinarchy-host/os-release`
  for distribution facts. If that mount is unavailable, the Agent omits OS
  distribution fields instead of reporting the container image's OS.
- Omits root filesystem capacity when running in Docker/Podman because the
  container's `/` is not the host root filesystem.
- Added `compose.systemd.yaml` as a separate opt-in overlay. It mounts the host
  system bus read-only and adds the bus socket's owning GID as a supplemental
  group.
- Added host preparation and removal scripts. For control mode, the host
  preparation script creates a Polkit rule for the host user `machagent` (UID
  10001) limited to `start`, `stop`, and `restart` on explicit service units.
- Kept service management and service control disabled in the default Compose
  profile. Service control requires both the Agent's exact allowlist and the
  matching host Polkit rule.
- Updated GHCR publishing to build and publish AMD64 and ARM64 images.

## Security boundaries

The optional system-bus mount exposes a sensitive host interface to the Agent
container. Polkit restricts systemd unit state changes but is not a general
D-Bus proxy. Do not enable this mode unless the operator accepts the additional
system-bus exposure and trusts the Agent image. This initial integration assumes
rootful Docker without user-namespace remapping for service control.

## Verification

- `make fmt-check`: passed.
- `make deployment-check`: passed, including native release packaging,
  systemd unit syntax and Docker deployment static checks.
- Shared protocol tests: passed in the isolated Go 1.23 harness.
- Agent unit tests and race detector: passed in the isolated Go 1.23 harness.
- `go vet`, build, configuration check and Windows test cross-compilation:
  passed in the isolated harness.
- Compose files and Docker GitHub Actions workflow parsed as YAML.

## Not verified here

Docker/BuildKit is not installed in the execution environment. The following
still require testing on clean Linux hosts of both architectures:

- Docker image build and startup.
- Host UTS and host `/etc/os-release` behavior.
- D-Bus socket connectivity from the unprivileged container.
- Polkit allow/deny behavior for permitted and forbidden units/actions.
- Host systemd service status and lifecycle operations.
- Real NATS v1.55.0, Go 1.26 and controller end-to-end compatibility.

Do not describe Docker service management as production-verified until these
live checks pass.
