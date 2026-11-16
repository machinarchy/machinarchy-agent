#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTOCOL_DIR="${PROTOCOL_DIR:-$(cd "${ROOT_DIR}/../machinarchy-protocol" 2>/dev/null && pwd || true)}"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "${WORK_DIR}"' EXIT

mkdir -p "${WORK_DIR}/agent/cmd" "${WORK_DIR}/agent/internal" \
  "${WORK_DIR}/protocol/agent" "${WORK_DIR}/protocol/execution" "${WORK_DIR}/protocol/hostfacts" "${WORK_DIR}/protocol/services" "${WORK_DIR}/testdeps/nats.go"
cp -R "${ROOT_DIR}/cmd/machinarchy-agent" "${WORK_DIR}/agent/cmd/"
cp -R "${ROOT_DIR}/internal/agent" "${WORK_DIR}/agent/internal/"
if [[ -z "${PROTOCOL_DIR}" || ! -d "${PROTOCOL_DIR}/agent/v1" ]]; then
  echo "machinarchy-protocol checkout not found; set PROTOCOL_DIR to its repository path." >&2
  exit 1
fi
cp -R "${PROTOCOL_DIR}/agent/v1" "${WORK_DIR}/protocol/agent/"
cp -R "${PROTOCOL_DIR}/execution/v1" "${WORK_DIR}/protocol/execution/"
cp -R "${PROTOCOL_DIR}/hostfacts/v1" "${WORK_DIR}/protocol/hostfacts/"
cp -R "${PROTOCOL_DIR}/services/v1" "${WORK_DIR}/protocol/services/"
cp -R "${ROOT_DIR}/testsupport/nats.go/." "${WORK_DIR}/testdeps/nats.go/"

cat > "${WORK_DIR}/agent/go.mod" <<'MOD'
module github.com/machinarchy/machinarchy-agent

go 1.23

require (
	github.com/machinarchy/machinarchy-protocol v0.0.0
	github.com/nats-io/nats.go v0.0.0
)

replace github.com/machinarchy/machinarchy-protocol => ../protocol
replace github.com/nats-io/nats.go => ../testdeps/nats.go
MOD

cat > "${WORK_DIR}/protocol/go.mod" <<'MOD'
module github.com/machinarchy/machinarchy-protocol

go 1.23
MOD

cd "${WORK_DIR}/protocol"
printf '%s\n' '== shared protocol package tests =='
go test -count=1 ./...
printf '%s\n' '== shared protocol go vet =='
go vet ./...
cd "${WORK_DIR}/agent"
printf '%s\n' '== unit tests (stub NATS transport) =='
go test -count=1 ./...
printf '%s\n' '== race detector =='
go test -race -count=1 ./...
printf '%s\n' '== go vet =='
go vet ./...
printf '%s\n' '== build =='
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
GOOS=windows GOARCH=amd64 go test -c -o "${WORK_DIR}/agent-windows.test.exe" ./internal/agent
printf '%s\n' 'Isolated verification completed. This does not replace testing against the real NATS module or a live controller.'
