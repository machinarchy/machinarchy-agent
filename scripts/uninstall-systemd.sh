#!/usr/bin/env bash
set -euo pipefail

readonly SERVICE_NAME="machinarchy-agent"
readonly AGENT_USER="machagent"
readonly AGENT_GROUP="machagent"
readonly BIN_DEST="/usr/local/bin/machinarchy-agent"
readonly UNIT_DEST="/etc/systemd/system/machinarchy-agent.service"
readonly CONFIG_DIR="/etc/machinarchy-agent"
readonly STATE_DIR="/var/lib/machinarchy-agent"
readonly BACKUP_DIR="/usr/local/lib/machinarchy-agent"

purge=0
yes=0
while (($#)); do
  case "$1" in
    --purge) purge=1; shift ;;
    --yes) yes=1; shift ;;
    -h|--help)
      echo "Usage: sudo ./uninstall-systemd.sh [--purge [--yes]]"
      echo "Default: remove service and binaries, preserving configuration and state."
      echo "--purge: also permanently remove configuration, credentials and Agent state."
      exit 0
      ;;
    *) echo "Unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run this script as root." >&2
  exit 1
fi
if ((yes && !purge)); then
  echo "--yes is only valid together with --purge." >&2
  exit 2
fi
if ((purge && !yes)); then
  echo "PURGE requested: this permanently deletes ${CONFIG_DIR} and ${STATE_DIR}, including credentials and Agent identity."
  read -r -p 'Type PURGE to continue: ' confirmation
  [[ "${confirmation}" == "PURGE" ]] || { echo "Uninstall cancelled."; exit 1; }
fi

command -v systemctl >/dev/null 2>&1 || { echo "systemctl is required for native uninstall." >&2; exit 1; }
systemctl show-environment >/dev/null 2>&1 || { echo "The system systemd manager is not available; refusing to remove the service executable." >&2; exit 1; }
if systemctl is-active --quiet "${SERVICE_NAME}"; then
  systemctl stop "${SERVICE_NAME}" || {
    echo "Could not stop ${SERVICE_NAME}; refusing to remove its executable." >&2
    exit 1
  }
fi
if systemctl is-enabled --quiet "${SERVICE_NAME}"; then
  systemctl disable "${SERVICE_NAME}" || {
    echo "Could not disable ${SERVICE_NAME}; refusing to continue uninstall." >&2
    exit 1
  }
fi
rm -f -- "${UNIT_DEST}"
systemctl daemon-reload
systemctl reset-failed "${SERVICE_NAME}" >/dev/null 2>&1 || true
rm -f -- "${BIN_DEST}"
rm -rf -- "${BACKUP_DIR}"

if ((purge)); then
  rm -rf -- "${CONFIG_DIR}" "${STATE_DIR}"
  if command -v userdel >/dev/null 2>&1 && getent passwd "${AGENT_USER}" >/dev/null; then
    userdel "${AGENT_USER}"
  fi
  if command -v groupdel >/dev/null 2>&1 && getent group "${AGENT_GROUP}" >/dev/null; then
    groupdel "${AGENT_GROUP}" || true
  fi
  echo "Machinarchy Agent removed, including configuration and persistent state."
else
  echo "Machinarchy Agent service and binaries removed."
  echo "Configuration preserved at ${CONFIG_DIR}; state preserved at ${STATE_DIR}."
  echo "The ${AGENT_USER} account and ${AGENT_GROUP} group were preserved."
fi
