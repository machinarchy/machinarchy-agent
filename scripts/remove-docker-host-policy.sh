#!/usr/bin/env bash
set -euo pipefail

RULE="/etc/polkit-1/rules.d/50-machinarchy-agent.rules"
if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  echo "Usage: sudo scripts/remove-docker-host-policy.sh"
  echo "Removes the Machinarchy Agent Polkit rule. It does not delete the machagent host account."
  exit 0
fi
if (( EUID != 0 )); then
  echo "Run this script as root (for example, with sudo)." >&2
  exit 1
fi
if [[ -e "$RULE" ]]; then
  rm -f -- "$RULE"
  echo "Removed ${RULE}"
else
  echo "No Machinarchy Agent Polkit rule was installed."
fi
