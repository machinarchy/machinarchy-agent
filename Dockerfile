# syntax=docker/dockerfile:1
#
# Base images are pinned by digest (multi-arch index); the tag is in the
# comment above each FROM. Dependabot (docker ecosystem) proposes updates.

# golang:1.27.4-bookworm
FROM --platform=$BUILDPLATFORM golang@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src
COPY go.mod go.sum ./
COPY protocol/go.mod ./protocol/go.mod
RUN --mount=type=cache,target=/go/pkg/mod go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
COPY protocol ./protocol

# Reproducible: no VCS stamping, trimmed paths, version metadata only from
# these arguments (BUILD_TIME should be the commit time).
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /out && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildTime=${BUILD_TIME}" \
      -o /out/machinarchy-agent ./cmd/machinarchy-agent

# Debian slim rather than distroless: service management shells out to the
# host's systemd through systemctl over the mounted D-Bus socket.
# debian:bookworm-slim
FROM debian@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc AS runtime

RUN apt-get update \
    && apt-get install --no-install-recommends -y ca-certificates systemd \
    && rm -rf /var/lib/apt/lists/* \
    && test -x /usr/bin/systemctl \
    && groupadd --system --gid 10001 machagent \
    && useradd --system --uid 10001 --gid machagent --home-dir /var/lib/machinarchy-agent --shell /usr/sbin/nologin machagent \
    && install -d -o machagent -g machagent -m 0700 /var/lib/machinarchy-agent \
    && install -d -o root -g root -m 0555 /run/machinarchy-host

COPY --from=build /out/machinarchy-agent /usr/local/bin/machinarchy-agent

ENV MACHINARCHY_ENVIRONMENT=production \
    MACHINARCHY_AGENT_STATE_FILE=/var/lib/machinarchy-agent/state.json \
    MACHINARCHY_AGENT_SYSTEMCTL_PATH=/usr/bin/systemctl \
    DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/dbus/system_bus_socket

USER 10001:10001
STOPSIGNAL SIGTERM
# Healthy while the agent is enrolled and holds an unexpired lease (read from
# the state file; the agent serves no HTTP).
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
    CMD ["/usr/local/bin/machinarchy-agent", "--healthcheck"]
ENTRYPOINT ["/usr/local/bin/machinarchy-agent"]
