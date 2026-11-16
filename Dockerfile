# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
ARG TARGETARCH

WORKDIR /src
COPY go.mod ./
RUN --mount=type=secret,id=protocol_token,required=false \
    set -eu; \
    MODULE_TOKEN=""; \
    if [ -s /run/secrets/protocol_token ]; then \
      MODULE_TOKEN="$(cat /run/secrets/protocol_token)"; \
      export GOPRIVATE=github.com/machinarchy/machinarchy-protocol; \
      git config --global url."https://x-access-token:${MODULE_TOKEN}@github.com/".insteadOf "https://github.com/"; \
    fi; \
    go mod download; \
    if [ -n "${MODULE_TOKEN}" ]; then \
      git config --global --unset-all url."https://x-access-token:${MODULE_TOKEN}@github.com/".insteadOf || true; \
    fi
COPY . .
RUN mkdir -p /out && CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildTime=${BUILD_TIME}" \
    -o /out/machinarchy-agent ./cmd/machinarchy-agent

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
    MACHINARCHY_AGENT_STATE_FILE=/var/lib/machinarchy-agent/identity.json \
    MACHINARCHY_AGENT_OPERATION_STORE_FILE=/var/lib/machinarchy-agent/operations.json \
    MACHINARCHY_AGENT_SYSTEMCTL_PATH=/usr/bin/systemctl \
    DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/dbus/system_bus_socket

USER 10001:10001
ENTRYPOINT ["/usr/local/bin/machinarchy-agent"]
