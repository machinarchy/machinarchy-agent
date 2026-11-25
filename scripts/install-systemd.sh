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

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
if [[ -x "${SCRIPT_DIR}/machinarchy-agent" ]]; then
  BIN_SOURCE="${SCRIPT_DIR}/machinarchy-agent"
elif [[ -x "${SCRIPT_DIR}/../bin/machinarchy-agent" ]]; then
  BIN_SOURCE="${SCRIPT_DIR}/../bin/machinarchy-agent"
else
  BIN_SOURCE="${SCRIPT_DIR}/machinarchy-agent"
fi
if [[ -f "${SCRIPT_DIR}/machinarchy-agent.service" ]]; then
  SERVICE_SOURCE="${SCRIPT_DIR}/machinarchy-agent.service"
else
  SERVICE_SOURCE="${SCRIPT_DIR}/../deployments/systemd/machinarchy-agent.service"
fi

usage() {
  cat <<'USAGE'
Usage: sudo ./install-systemd.sh [--binary PATH] [--service PATH]

Install or upgrade the Machinarchy Agent systemd service. Existing configuration
and identity/operation state are preserved. If an older binary is installed, it
is saved as a one-step rollback at /usr/local/lib/machinarchy-agent/machinarchy-agent.previous.
USAGE
}

while (($#)); do
  case "$1" in
    --binary)
      [[ $# -ge 2 ]] || { echo "--binary requires a path" >&2; exit 2; }
      BIN_SOURCE="$2"
      shift 2
      ;;
    --service)
      [[ $# -ge 2 ]] || { echo "--service requires a path" >&2; exit 2; }
      SERVICE_SOURCE="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run this script as root (for example: sudo ./install-systemd.sh)." >&2
  exit 1
fi

for command_name in getent groupadd useradd install systemctl mktemp mv cp chmod chown cut rm uname; do
  command -v "${command_name}" >/dev/null 2>&1 || {
    echo "Required command not found: ${command_name}" >&2
    exit 1
  }
done
systemctl show-environment >/dev/null 2>&1 || {
  echo "The system systemd manager is not available; refusing to install a systemd service." >&2
  exit 1
}

[[ -f "${BIN_SOURCE}" && -x "${BIN_SOURCE}" ]] || {
  echo "Executable Agent binary not found: ${BIN_SOURCE}" >&2
  exit 1
}
[[ -f "${SERVICE_SOURCE}" ]] || {
  echo "systemd unit file not found: ${SERVICE_SOURCE}" >&2
  exit 1
}

# Release archives carry target metadata. Refuse an accidental cross-architecture install.
if [[ -f "${SCRIPT_DIR}/BUILD-INFO.txt" ]]; then
  release_os=""
  release_arch=""
  while IFS='=' read -r key value; do
    case "${key}" in
      os) release_os="${value}" ;;
      arch) release_arch="${value}" ;;
    esac
  done < "${SCRIPT_DIR}/BUILD-INFO.txt"
  actual_arch="$(uname -m)"
  case "${actual_arch}" in
    x86_64|amd64) actual_arch="amd64" ;;
    aarch64|arm64) actual_arch="arm64" ;;
  esac
  if [[ "${release_os}" != "linux" || "${release_arch}" != "${actual_arch}" ]]; then
    echo "Release archive target ${release_os}/${release_arch} does not match this host (linux/${actual_arch})." >&2
    exit 1
  fi
fi

# Refuse to install over an unrelated account. Never force a fixed UID/GID.
NOLOGIN_SHELL=""
for candidate in /usr/sbin/nologin /sbin/nologin /usr/bin/nologin /bin/false; do
  if [[ -x "${candidate}" ]]; then
    NOLOGIN_SHELL="${candidate}"
    break
  fi
done
[[ -n "${NOLOGIN_SHELL}" ]] || { echo "Could not find a non-interactive login shell." >&2; exit 1; }

if ! getent group "${AGENT_GROUP}" >/dev/null; then
  groupadd --system "${AGENT_GROUP}"
fi
GROUP_GID="$(getent group "${AGENT_GROUP}" | cut -d: -f3)"
if getent passwd "${AGENT_USER}" >/dev/null; then
  USER_ENTRY="$(getent passwd "${AGENT_USER}")"
  USER_GID="$(cut -d: -f4 <<< "${USER_ENTRY}")"
  USER_SHELL="$(cut -d: -f7 <<< "${USER_ENTRY}")"
  if [[ "${USER_GID}" != "${GROUP_GID}" ]]; then
    echo "Existing ${AGENT_USER} account does not use the ${AGENT_GROUP} primary group; refusing to change it." >&2
    exit 1
  fi
  case "${USER_SHELL}" in
    */nologin|*/false) ;;
    *)
      echo "Existing ${AGENT_USER} account has an interactive shell (${USER_SHELL}); refusing to use it for the Agent." >&2
      exit 1
      ;;
  esac
else
  useradd --system --gid "${AGENT_GROUP}" --home-dir "${STATE_DIR}" --no-create-home --shell "${NOLOGIN_SHELL}" "${AGENT_USER}"
fi

install -d -o root -g "${AGENT_GROUP}" -m 0750 "${CONFIG_DIR}"
install -d -o "${AGENT_USER}" -g "${AGENT_GROUP}" -m 0700 "${STATE_DIR}"
install -d -o root -g root -m 0755 "$(dirname -- "${BIN_DEST}")" "${BACKUP_DIR}" "$(dirname -- "${UNIT_DEST}")"

# Seed a safe template only on first install; never overwrite operator config.
if [[ ! -e "${CONFIG_DIR}/agent.env" ]]; then
  if [[ -f "${SCRIPT_DIR}/agent.env.example" ]]; then
    install -o root -g "${AGENT_GROUP}" -m 0640 "${SCRIPT_DIR}/agent.env.example" "${CONFIG_DIR}/agent.env"
  else
    install -o root -g "${AGENT_GROUP}" -m 0640 /dev/null "${CONFIG_DIR}/agent.env"
  fi
else
  chown root:"${AGENT_GROUP}" "${CONFIG_DIR}/agent.env"
  chmod 0640 "${CONFIG_DIR}/agent.env"
fi

was_active=0
if systemctl is-active --quiet "${SERVICE_NAME}"; then
  was_active=1
fi

backup_binary="${BACKUP_DIR}/machinarchy-agent.previous"
backup_unit="${BACKUP_DIR}/machinarchy-agent.service.previous"
had_binary=0
had_unit=0
if [[ -f "${BIN_DEST}" ]]; then
  had_binary=1
  temp_backup="$(mktemp "${BACKUP_DIR}/.agent.previous.XXXXXX")"
  install -o root -g root -m 0755 "${BIN_DEST}" "${temp_backup}"
  mv -f -- "${temp_backup}" "${backup_binary}"
fi
if [[ -f "${UNIT_DEST}" ]]; then
  had_unit=1
  temp_unit_backup="$(mktemp "${BACKUP_DIR}/.unit.previous.XXXXXX")"
  install -o root -g root -m 0644 "${UNIT_DEST}" "${temp_unit_backup}"
  mv -f -- "${temp_unit_backup}" "${backup_unit}"
fi

# Replace executable and unit atomically on their respective filesystems.
temp_binary="$(mktemp "$(dirname -- "${BIN_DEST}")/.machinarchy-agent.XXXXXX")"
temp_unit="$(mktemp "$(dirname -- "${UNIT_DEST}")/.machinarchy-agent.service.XXXXXX")"
cleanup() {
  [[ -z "${temp_binary:-}" ]] || rm -f -- "${temp_binary}"
  [[ -z "${temp_unit:-}" ]] || rm -f -- "${temp_unit}"
}
trap cleanup EXIT
install -o root -g root -m 0755 "${BIN_SOURCE}" "${temp_binary}"
install -o root -g root -m 0644 "${SERVICE_SOURCE}" "${temp_unit}"
mv -f -- "${temp_binary}" "${BIN_DEST}"
temp_binary=""
mv -f -- "${temp_unit}" "${UNIT_DEST}"
temp_unit=""

systemctl daemon-reload
if ((was_active)); then
  if ! systemctl restart "${SERVICE_NAME}"; then
    echo "Agent restart failed; attempting to restore the previous installation." >&2
    if ((had_binary)); then
      restore_binary="$(mktemp "$(dirname -- "${BIN_DEST}")/.machinarchy-agent.restore.XXXXXX")"
      install -o root -g root -m 0755 "${backup_binary}" "${restore_binary}"
      mv -f -- "${restore_binary}" "${BIN_DEST}"
    fi
    if ((had_unit)); then
      restore_unit="$(mktemp "$(dirname -- "${UNIT_DEST}")/.machinarchy-agent.service.restore.XXXXXX")"
      install -o root -g root -m 0644 "${backup_unit}" "${restore_unit}"
      mv -f -- "${restore_unit}" "${UNIT_DEST}"
    fi
    systemctl daemon-reload
    if ((had_binary && had_unit)); then
      systemctl restart "${SERVICE_NAME}" || true
    else
      systemctl stop "${SERVICE_NAME}" || true
    fi
    exit 1
  fi
fi

echo "Machinarchy Agent installed at ${BIN_DEST}."
echo "Configuration: ${CONFIG_DIR}/agent.env (preserved if it already existed)."
echo "Persistent state: ${STATE_DIR} (preserved)."
if ((was_active)); then
  echo "The previously active service has been restarted."
else
  echo "The service was not started. Review the configuration, then run:"
  echo "  systemctl enable --now ${SERVICE_NAME}"
fi
if ((had_binary && had_unit)); then
  echo "Previous binary and unit saved under ${BACKUP_DIR}; run rollback-systemd.sh to restore them."
elif ((had_binary)); then
  echo "Previous binary saved at ${backup_binary}, but no previous unit was found; use a verified release archive for rollback."
fi
