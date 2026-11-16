#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTOCOL_DIR="${PROTOCOL_DIR:-${ROOT_DIR}/protocol}"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "${WORK_DIR}"' EXIT

if [[ ! -f "${PROTOCOL_DIR}/go.mod" || ! -d "${PROTOCOL_DIR}/agent/v1" ]]; then
  echo "Bundled protocol module not found at ${PROTOCOL_DIR}." >&2
  exit 1
fi

mkdir -p "${WORK_DIR}/agent/cmd" "${WORK_DIR}/agent/internal" \
  "${WORK_DIR}/protocol" "${WORK_DIR}/testdeps/nats.go"
cp -R "${ROOT_DIR}/cmd/machinarchy-agent" "${WORK_DIR}/agent/cmd/"
cp -R "${ROOT_DIR}/internal/." "${WORK_DIR}/agent/internal/"
cp -R "${PROTOCOL_DIR}/agent" "${PROTOCOL_DIR}/execution" \
  "${PROTOCOL_DIR}/hostfacts" "${PROTOCOL_DIR}/services" "${WORK_DIR}/protocol/"
cp "${ROOT_DIR}/testsupport/nats.go/go.mod" "${ROOT_DIR}/testsupport/nats.go/nats.go" \
  "${WORK_DIR}/testdeps/nats.go/"
cp -R "${ROOT_DIR}/testsupport/nats.go/jetstream" "${WORK_DIR}/testdeps/nats.go/"

cat > "${WORK_DIR}/agent/go.mod" <<'MOD'
module github.com/machinarchy/machinarchy-agent

go 1.23

require (
	github.com/machinarchy/machinarchy-agent/protocol v0.0.0
	github.com/nats-io/nats.go v0.0.0
)

replace github.com/machinarchy/machinarchy-agent/protocol => ../protocol
replace github.com/nats-io/nats.go => ../testdeps/nats.go
MOD

cat > "${WORK_DIR}/protocol/go.mod" <<'MOD'
module github.com/machinarchy/machinarchy-agent/protocol

go 1.23
MOD

cd "${WORK_DIR}/protocol"
printf '%s\n' '== shared protocol package tests =='
go test -count=1 ./...
printf '%s\n' '== shared protocol go vet =='
go vet ./...
cd "${WORK_DIR}/agent"
printf '%s\n' '== Agent unit tests (stub NATS transport) =='
go test -count=1 ./...
printf '%s\n' '== Agent race detector =='
go test -race -count=1 ./...
printf '%s\n' '== Agent go vet =='
go vet ./...
printf '%s\n' '== Agent build =='
go build -o "${WORK_DIR}/machinarchy-agent" ./cmd/machinarchy-agent
printf '%s\n' '== CLI metadata and configuration validation =='
"${WORK_DIR}/machinarchy-agent" --version
MACHINARCHY_AGENT_NATS_URL=nats://127.0.0.1:4222 \
  MACHINARCHY_AGENT_STATE_FILE="${WORK_DIR}/identity.json" \
  "${WORK_DIR}/machinarchy-agent" --check-config
if "${WORK_DIR}/machinarchy-agent" --version --check-config >/dev/null 2>&1; then
  echo "--version and --check-config should not be accepted together" >&2
  exit 1
fi
printf '%s\n' '== Windows cross-compile =='
GOOS=windows GOARCH=amd64 go build ./...
printf '%s\n' 'Isolated verification completed. This does not replace testing against the real NATS module or a live controller.'
