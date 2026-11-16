# Docker Deployment

> **Current limitation:** this image runs the Agent process inside a container;
> it is not yet a supported host OS-management deployment. A normal container
> sees its own filesystem and does not automatically control the host's
> systemd manager. Keep `MACHINARCHY_AGENT_SERVICE_MANAGEMENT_ENABLED=false`
> and `MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED=false` in Docker deployments
> until the dedicated host-integration phase is implemented and verified.


Build:

```bash
docker build -t machinarchy-agent:dev .
```

The image runs as UID/GID `10001`, uses a private state directory at
`/var/lib/machinarchy-agent`, and contains the agent binary plus CA certificates.

Run with a protected environment file and persistent state volume:

```bash
docker volume create machinarchy-agent-state
docker run --rm \
  --env-file /etc/machinarchy-agent/agent.env \
  -v machinarchy-agent-state:/var/lib/machinarchy-agent \
  machinarchy-agent:dev
```

Ensure the mounted volume is owned by UID/GID `10001` and has mode `0700`.
Do not bake NATS credentials or enrollment tokens into image layers. Inject the
one-time enrollment token through a secret manager and remove it after
successful enrollment.

`docker-compose.yml` starts a local NATS server for development only. It does
not start a controller or a working agent service because registration and
lease handlers must exist in the controller first.


If the protocol repository is private, the build needs a read-only GitHub
credential for the module fetch. Do not pass that credential as a Docker build
argument or bake it into an image layer; use a BuildKit secret mount and a
short-lived, repository-scoped token. If the protocol repository is public, no
extra credential is needed.
