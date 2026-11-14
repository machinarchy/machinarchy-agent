#!/usr/bin/env bash
set -euo pipefail

BIN_SOURCE="${1:-./bin/machinarchy-agent}"
SERVICE_SOURCE="$(dirname "$0")/../deployments/systemd/machinarchy-agent.service"

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run this script as root (sudo)." >&2
  exit 1
fi
if [[ ! -f "${BIN_SOURCE}" ]]; then
  echo "Agent binary not found: ${BIN_SOURCE}" >&2
  exit 1
fi

getent group machagent >/dev/null || groupadd --system --gid 10001 machagent
getent passwd machagent >/dev/null || useradd --system --uid 10001 --gid machagent --home-dir /var/lib/machinarchy-agent --shell /usr/sbin/nologin machagent
install -d -o root -g root -m 0755 /usr/local/bin
install -m 0755 "${BIN_SOURCE}" /usr/local/bin/machinarchy-agent
install -d -o root -g root -m 0750 /etc/machinarchy-agent
install -d -o machagent -g machagent -m 0700 /var/lib/machinarchy-agent
install -m 0644 "${SERVICE_SOURCE}" /etc/systemd/system/machinarchy-agent.service

systemctl daemon-reload
echo "Installed Machinarchy Agent. Create /etc/machinarchy-agent/agent.env, then run:"
echo "  systemctl enable --now machinarchy-agent"
