#!/usr/bin/env bash
set -euo pipefail

readonly SERVICE_NAME="machinarchy-agent"
readonly BIN_DEST="/usr/local/bin/machinarchy-agent"
readonly UNIT_DEST="/etc/systemd/system/machinarchy-agent.service"
readonly BACKUP_DIR="/usr/local/lib/machinarchy-agent"
readonly BACKUP_BIN="${BACKUP_DIR}/machinarchy-agent.previous"
readonly BACKUP_UNIT="${BACKUP_DIR}/machinarchy-agent.service.previous"

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run this script as root." >&2
  exit 1
fi
for path in "${BIN_DEST}" "${UNIT_DEST}" "${BACKUP_BIN}" "${BACKUP_UNIT}"; do
  [[ -f "${path}" ]] || { echo "Required rollback file is missing: ${path}" >&2; exit 1; }
done
command -v systemctl >/dev/null 2>&1 || { echo "systemctl is required for native rollback." >&2; exit 1; }
systemctl show-environment >/dev/null 2>&1 || { echo "The system systemd manager is not available; refusing rollback." >&2; exit 1; }

was_active=0
if systemctl is-active --quiet "${SERVICE_NAME}"; then
  was_active=1
fi

# Preserve the currently installed build as the next rollback target.
temp_current_bin="$(mktemp "${BACKUP_DIR}/.current-bin.XXXXXX")"
temp_current_unit="$(mktemp "${BACKUP_DIR}/.current-unit.XXXXXX")"
install -o root -g root -m 0755 "${BIN_DEST}" "${temp_current_bin}"
install -o root -g root -m 0644 "${UNIT_DEST}" "${temp_current_unit}"

temp_bin="$(mktemp "$(dirname -- "${BIN_DEST}")/.machinarchy-agent.rollback.XXXXXX")"
temp_unit="$(mktemp "$(dirname -- "${UNIT_DEST}")/.machinarchy-agent.service.rollback.XXXXXX")"
install -o root -g root -m 0755 "${BACKUP_BIN}" "${temp_bin}"
install -o root -g root -m 0644 "${BACKUP_UNIT}" "${temp_unit}"
mv -f -- "${temp_bin}" "${BIN_DEST}"
mv -f -- "${temp_unit}" "${UNIT_DEST}"
mv -f -- "${temp_current_bin}" "${BACKUP_BIN}"
mv -f -- "${temp_current_unit}" "${BACKUP_UNIT}"

systemctl daemon-reload
if ((was_active)) && ! systemctl restart "${SERVICE_NAME}"; then
  echo "Rollback service restart failed; restoring the build that was active before rollback." >&2
  restore_bin="$(mktemp "$(dirname -- "${BIN_DEST}")/.machinarchy-agent.restore.XXXXXX")"
  restore_unit="$(mktemp "$(dirname -- "${UNIT_DEST}")/.machinarchy-agent.service.restore.XXXXXX")"
  install -o root -g root -m 0755 "${BACKUP_BIN}" "${restore_bin}"
  install -o root -g root -m 0644 "${BACKUP_UNIT}" "${restore_unit}"
  mv -f -- "${restore_bin}" "${BIN_DEST}"
  mv -f -- "${restore_unit}" "${UNIT_DEST}"
  systemctl daemon-reload
  systemctl restart "${SERVICE_NAME}" || true
  exit 1
fi

echo "Machinarchy Agent rolled back. The previous current build is retained as the next rollback target."
