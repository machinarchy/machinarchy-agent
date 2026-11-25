#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Prepare a Linux host for optional Machinarchy Agent Docker service integration.

Usage:
  sudo scripts/prepare-docker-host.sh --status-only
  sudo scripts/prepare-docker-host.sh SERVICE.service [SERVICE.service ...]

--status-only validates the system D-Bus socket and prints its owning GID.
Passing service names additionally requires Polkit, reserves host UID 10001
for the container's machagent identity, and installs a Polkit rule allowing
only start/stop/restart for the listed units.

Neither mode enables service control in the Agent. Keep
MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED=false until you explicitly approve it.
USAGE
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  usage
  exit 0
fi

status_only=0
if [[ "${1:-}" == "--status-only" ]]; then
  if [[ $# -ne 1 ]]; then
    echo "--status-only cannot be combined with service names." >&2
    exit 2
  fi
  status_only=1
  shift
fi

if (( status_only == 0 )) && [[ $# -lt 1 || $# -gt 64 ]]; then
  usage >&2
  exit 2
fi

services=()
declare -A seen=()
for unit in "$@"; do
  if [[ ! "$unit" =~ ^[A-Za-z0-9_.@-]+\.service$ ]] || \
    [[ "$unit" == -* || "$unit" == .* || "$unit" == *..* || ${#unit} -gt 255 ]]; then
    echo "Invalid service unit name: ${unit}" >&2
    exit 2
  fi
  if [[ -n "${seen[$unit]:-}" ]]; then
    echo "Duplicate service unit: ${unit}" >&2
    exit 2
  fi
  seen[$unit]=1
  services+=("$unit")
done

if (( EUID != 0 )); then
  echo "Run this host preparation script as root (for example, with sudo)." >&2
  exit 1
fi

DBUS_SOCKET="${DBUS_SYSTEM_BUS_SOCKET:-/run/dbus/system_bus_socket}"
POLKIT_RULE="/etc/polkit-1/rules.d/50-machinarchy-agent.rules"
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/lib/docker-host.sh"

if [[ ! -S "$DBUS_SOCKET" ]]; then
  echo "Host system D-Bus socket not found: ${DBUS_SOCKET}" >&2
  exit 1
fi
socket_gid="$(stat -c '%g' "$DBUS_SOCKET")"
socket_mode="$(stat -c '%a' "$DBUS_SOCKET")"
if ! dbus_gid="$(machinarchy_dbus_access_gid "$socket_gid" "$socket_mode")"; then
  echo "D-Bus socket must allow read/write access to other users or a non-root group." >&2
  echo "Refusing root-group-only socket access or insufficient socket permissions." >&2
  exit 1
fi

if (( status_only )); then
  printf 'MACHINARCHY_DBUS_GID=%s\n' "$dbus_gid"
  exit 0
fi

if ! command -v pkaction >/dev/null 2>&1; then
  echo "Polkit is not installed. Install and enable the host Polkit service first." >&2
  exit 1
fi
if [[ ! -d /etc/polkit-1/rules.d ]]; then
  echo "Polkit rules directory is missing: /etc/polkit-1/rules.d" >&2
  exit 1
fi
if ! pkaction --action-id org.freedesktop.systemd1.manage-units >/dev/null 2>&1; then
  echo "Polkit does not expose org.freedesktop.systemd1.manage-units on this host." >&2
  exit 1
fi
for unit in "${services[@]}"; do
  load_state="$(systemctl show --property=LoadState --value "$unit" 2>/dev/null || true)"
  if [[ "$load_state" != "loaded" ]]; then
    echo "Service unit is not loaded on this host: ${unit}" >&2
    exit 1
  fi
done

uid_owner="$(getent passwd 10001 | cut -d: -f1 || true)"
name_uid="$(id -u machagent 2>/dev/null || true)"
if [[ -n "$uid_owner" && "$uid_owner" != "machagent" ]]; then
  echo "Host UID 10001 is already assigned to ${uid_owner}; cannot map container identity safely." >&2
  exit 1
fi
if [[ -n "$name_uid" && "$name_uid" != "10001" ]]; then
  echo "Host account machagent already exists with UID ${name_uid}; expected UID 10001." >&2
  exit 1
fi
if [[ -z "$name_uid" ]]; then
  useradd --uid 10001 --no-create-home --home-dir /nonexistent \
    --shell /usr/sbin/nologin --comment "Machinarchy Agent container identity" machagent
fi

umask 077
tmp_rule="$(mktemp /etc/polkit-1/rules.d/.50-machinarchy-agent.XXXXXX)"
trap 'rm -f "$tmp_rule"' EXIT
{
  printf '%s\n' 'polkit.addRule(function(action, subject) {'
  printf '%s\n' '  if (action.id !== "org.freedesktop.systemd1.manage-units") return polkit.Result.NOT_HANDLED;'
  printf '%s\n' '  if (subject.user !== "machagent") return polkit.Result.NOT_HANDLED;'
  printf '%s\n' '  var unit = action.lookup("unit");'
  printf '  var allowedUnits = ['
  first=1
  for unit in "${services[@]}"; do
    if (( first )); then first=0; else printf ', '; fi
    printf '"%s"' "$unit"
  done
  printf '%s\n' '];'
  printf '%s\n' '  if (allowedUnits.indexOf(unit) === -1) return polkit.Result.NOT_HANDLED;'
  printf '%s\n' '  var verb = action.lookup("verb");'
  printf '%s\n' '  if (["start", "stop", "restart"].indexOf(verb) === -1) return polkit.Result.NOT_HANDLED;'
  printf '%s\n' '  return polkit.Result.YES;'
  printf '%s\n' '});'
} > "$tmp_rule"
chmod 0644 "$tmp_rule"
mv -f "$tmp_rule" "$POLKIT_RULE"
trap - EXIT

printf 'Host policy installed for: %s\n' "${services[*]}"
printf 'MACHINARCHY_DBUS_GID=%s\n' "$dbus_gid"
printf '\nNext: set MACHINARCHY_DBUS_GID and the same service allowlist in .env.\n'
printf 'Keep MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED=false until you explicitly approve remote lifecycle actions.\n'
