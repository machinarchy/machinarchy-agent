# Docker Deployment

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
