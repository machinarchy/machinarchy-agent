# Docker Engine Deployment

Machinarchy Agent can run as a normal Docker container on a Linux host. This is
not a Kubernetes deployment and does not require Kubernetes, Helm, privileged
containers, the Docker socket, host PID, host networking, or a host-root mount.

## Deployment modes

### 1. Host facts only (default)

The default `compose.yaml` profile:

- runs as UID/GID `10001`;
- uses a persistent named volume for identity and operation state;
- uses a read-only container root filesystem and a small `/tmp` tmpfs;
- drops all Linux capabilities and enables `no-new-privileges`;
- shares only the host UTS namespace so the reported hostname is the host name;
- bind-mounts the host `/etc/os-release` read-only at
  `/run/machinarchy-host/os-release` so distribution facts are host facts;
- does not mount the host system D-Bus socket;
- keeps both service-management capabilities disabled.

The Agent uses the host kernel for kernel release, uptime and `/proc/meminfo`.
The reported logical CPU count is the CPU set visible to the container; if Docker
CPU limits are configured, it may represent the container allocation rather
than the full host CPU count. Host root-filesystem capacity is omitted inside a
container because `/` is the container overlay, not the host root filesystem.

Use Docker Engine on a Linux host and Docker Compose v2.15.1 or newer (the
`uts: host` Compose option is required for host hostname reporting).

Prepare a protected `.env` file and the secrets directory:

```bash
cat > .env <<'ENV'
MACHINARCHY_AGENT_SERVER_URL=https://machinarchy.example.internal
MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY=<base64 controller key>
MACHINARCHY_AGENT_ENROLLMENT_TOKEN_FILE=/run/secrets/enrollment.token
ENV
chmod 600 .env
mkdir -p secrets
install -o 10001 -g 10001 -m 0600 /path/to/enrollment.token secrets/enrollment.token
```

The default Compose profile mounts `./secrets` read-only at `/run/secrets`.
Secret files must be owned by UID 10001 with mode `0600`. After the logs show
`agent registered`, delete `secrets/enrollment.token`, remove
`MACHINARCHY_AGENT_ENROLLMENT_TOKEN_FILE` from `.env` and recreate the
container. The identity is kept in the `machinarchy-agent-state` volume. Make
sure the controller is reachable from the container network. A private CA
goes in `secrets/ca.pem` with `MACHINARCHY_AGENT_SERVER_CA_FILE=/run/secrets/ca.pem`.
Do not use `127.0.0.1` for a controller running on the Docker host; inside the container,
that address refers to the container itself.

Start the published GHCR image:

```bash
docker compose -f compose.yaml up -d
```

To build locally, build the image first and then point Compose at it:

```bash
docker build -t machinarchy-agent:local .
MACHINARCHY_AGENT_IMAGE=machinarchy-agent:local docker compose -f compose.yaml up -d
```

Inspect logs:

```bash
docker compose -f compose.yaml logs -f agent
```

The named volume `machinarchy-agent-state` retains the Agent identity and
operation store across container recreation. Do not delete it during routine
upgrades. The credentials bind mount is independent from the image and is not
copied into an image layer.

## 2. Optional host systemd management

The systemd integration is a separate opt-in overlay. It mounts the host
system bus socket into the Agent container and adds the socket's owning group
as a supplemental group. The host's systemd manager continues to run on the
host; systemd is not PID 1 inside the container. It also mounts an empty
tmpfs, owned by root and not writable by the agent, at `/run/systemd/system`: `systemctl` talks to the bus
only when that directory exists (its "booted with systemd" check), and without
it every `service.*` call fails with `System has not been booted with systemd`.
The host's own `/run/systemd` is not mounted.

Systemd grants unprivileged read access to its D-Bus API generally, while
operations that change unit state require the Polkit action
`org.freedesktop.systemd1.manage-units`. The host preparation script installs a
Polkit rule for the dedicated `machagent` identity (UID 10001) and grants only
`start`, `stop`, and `restart` for explicitly listed `.service` units. It does
not grant enable/disable, mask/unmask, daemon-reload, arbitrary unit-file
changes, or access to other units. See the systemd D-Bus security model:
<https://freedesktop.org/software/systemd/man/latest/org.freedesktop.systemd1.html>.

For read-only service status, prepare the D-Bus group without installing a
Polkit rule or creating a host account:

```bash
sudo scripts/prepare-docker-host.sh --status-only
```

For service lifecycle control, prepare the host as root and list only services
the Agent may control:

```bash
sudo scripts/prepare-docker-host.sh nginx.service ssh.service
```

Both modes require a system D-Bus socket accessible to the container UID. The
host helper prefers ordinary other-user read/write access (the common 0666
case) without adding a supplemental host group; when access is group-restricted,
it requires a non-root socket group with read/write permissions and adds only
that group. The control mode additionally requires Polkit
and systemd. It creates the host account
`machagent` with UID `10001` if that UID is free. It refuses to proceed if UID
10001 belongs to another account. Add the printed `MACHINARCHY_DBUS_GID` value
and the same exact allowlist to `.env`:

```dotenv
MACHINARCHY_DBUS_GID=107
MACHINARCHY_AGENT_SERVICE_ALLOWLIST=nginx.service,ssh.service
MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED=false
```

The GID above is only an example; use the value printed by the host script.
Protect `.env` with mode `0600`. Start the optional integration:

```bash
docker compose -f compose.yaml -f compose.systemd.yaml up -d
```

With `MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED=false`, the Agent exposes
read-only `service.status`. To enable start/stop/restart actions, review the
host Polkit rule and then explicitly set the variable to `true` before
recreating the container. Both the Agent's local allowlist and the host Polkit
rule must contain the same unit names. If either side does not permit an
operation, it must fail.

The D-Bus socket is a sensitive host interface. The container is not privileged,
but its process can connect to the host system bus. Use this mode only on hosts
where you accept that exposure and keep the container image and Agent supply
chain trusted. The Polkit rule limits systemd unit state changes, but it is not
a general D-Bus proxy and does not filter all other system-bus APIs.

This initial deployment supports ordinary rootful Docker with the default
UID/GID mapping. Rootless Docker and `userns-remap` are not supported yet:
they change ownership mapping for the credentials bind mount and, in service
mode, the D-Bus socket and Polkit identity. Do not assume either mode works
until those mappings have dedicated host tests.

Remove the host authorization when service control is no longer needed:

```bash
sudo scripts/remove-docker-host-policy.sh
```

This removes only the Polkit rule. It intentionally leaves the `machagent`
host account and Docker state in place; remove those manually only after
confirming no other deployment uses them.

## Runtime security boundaries

- No `privileged: true`.
- No Docker socket mount.
- No host PID namespace or host network mode.
- No host-root filesystem mount.
- Host `/etc/os-release` is mounted read-only at `/run/machinarchy-host/os-release`.
- Host system D-Bus is mounted only in the optional systemd overlay.
- The Agent runs as UID/GID `10001`, not root.
- Service management is disabled unless the override and local allowlist are configured.
- Service control is separately disabled by default and requires host Polkit policy.

## Image builds

The runtime image contains the `systemctl` client from Debian's `systemd`
package, but does not run the systemd daemon. The Agent uses the mounted host
system bus for optional service operations.

GitHub Actions publishes Linux `amd64` and `arm64` images. For local builds,
use the Dockerfile commands above. The build needs only public modules
(verified against `go.sum`) and copies only `cmd/`, `internal/` and `protocol/`.
