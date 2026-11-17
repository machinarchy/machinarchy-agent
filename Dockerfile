# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS build

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
COPY protocol ./protocol
RUN mkdir -p /out && CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildTime=${BUILD_TIME}" \
    -o /out/machinarchy-agent ./cmd/machinarchy-agent

# Debian slim rather than distroless: service management shells out to the
# host's systemd through systemctl over the mounted D-Bus socket.
FROM debian:bookworm-slim AS runtime

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
ENTRYPOINT ["/usr/local/bin/machinarchy-agent"]
