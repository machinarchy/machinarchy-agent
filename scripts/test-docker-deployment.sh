#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

for script in prepare-docker-host.sh remove-docker-host-policy.sh lib/docker-host.sh; do
  bash -n "${repo_root}/scripts/${script}"
done
source "${repo_root}/scripts/lib/docker-host.sh"
[[ "$(machinarchy_dbus_access_gid 0 666)" == "10001" ]]
[[ "$(machinarchy_dbus_access_gid 103 660)" == "103" ]]
if machinarchy_dbus_access_gid 0 660 >/dev/null 2>&1; then
  echo "D-Bus group selection accepted root-group-only socket access." >&2
  exit 1
fi
if machinarchy_dbus_access_gid 103 644 >/dev/null 2>&1; then
  echo "D-Bus group selection accepted insufficient socket permissions." >&2
  exit 1
fi

"${repo_root}/scripts/prepare-docker-host.sh" --help >/dev/null
"${repo_root}/scripts/remove-docker-host-policy.sh" --help >/dev/null
if "${repo_root}/scripts/prepare-docker-host.sh" --status-only unexpected >/dev/null 2>&1; then
  echo "--status-only accepted additional arguments." >&2
  exit 1
fi

for invalid_unit in 'nginx.service;evil' '../nginx.service' 'bad:unit.service' '-evil.service' 'foo..bar.service'; do
  if "${repo_root}/scripts/prepare-docker-host.sh" "$invalid_unit" >/dev/null 2>&1; then
    echo "Host preparation script accepted an invalid service unit name: ${invalid_unit}" >&2
    exit 1
  fi
done
if "${repo_root}/scripts/prepare-docker-host.sh" nginx.service nginx.service >/dev/null 2>&1; then
  echo "Host preparation script accepted a duplicate service unit." >&2
  exit 1
fi

compose="${repo_root}/compose.yaml"
overlay="${repo_root}/compose.systemd.yaml"
for required in \
  'user: "10001:10001"' \
  '/run/secrets/nats.creds' \
  'uts: host' \
  'read_only: true' \
  'no-new-privileges:true' \
  'MACHINARCHY_AGENT_SERVICE_MANAGEMENT_ENABLED: "false"' \
  'MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED: "false"'; do
  grep -Fq "$required" "$compose" || {
    echo "Default Compose profile missing required setting: ${required}" >&2
    exit 1
  }
done
if grep -Eq 'privileged:[[:space:]]*true|network_mode:[[:space:]]*host|pid:[[:space:]]*host|/var/run/docker.sock|/var/lib/docker' "$compose" "$overlay"; then
  echo "Docker deployment contains a forbidden privilege or host mount." >&2
  exit 1
fi
for required in \
  '/run/dbus/system_bus_socket' \
  'MACHINARCHY_DBUS_GID' \
  'MACHINARCHY_AGENT_SERVICE_MANAGEMENT_ENABLED: "true"' \
  'MACHINARCHY_AGENT_SERVICE_ALLOWLIST'; do
  grep -Fq "$required" "$overlay" || {
    echo "systemd Compose overlay missing required setting: ${required}" >&2
    exit 1
  }
done

grep -Fq 'org.freedesktop.systemd1.manage-units' "${repo_root}/scripts/prepare-docker-host.sh"
grep -Fq 'machinarchy_dbus_access_gid' "${repo_root}/scripts/prepare-docker-host.sh"
grep -Fq 'subject.user !== "machagent"' "${repo_root}/scripts/prepare-docker-host.sh"
grep -Fq '"start", "stop", "restart"' "${repo_root}/scripts/prepare-docker-host.sh"
grep -Fq 'allowedUnits.indexOf(unit)' "${repo_root}/scripts/prepare-docker-host.sh"
grep -Fq 'DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/dbus/system_bus_socket' "${repo_root}/internal/agent/services_linux.go"
grep -Fq 'apt-get install --no-install-recommends -y ca-certificates systemd' "${repo_root}/Dockerfile"
grep -Fq 'if isContainerEnvironmentAt("/")' "${repo_root}/internal/agent/facts_linux.go"
grep -Fq 'return "/run/machinarchy-host/os-release"' "${repo_root}/internal/agent/facts_linux.go"
grep -Fq '/run/machinarchy-host/os-release' "${compose}"
grep -Fq 'USER 10001:10001' "${repo_root}/Dockerfile"
grep -Fq 'install -d -o root -g root -m 0555 /run/machinarchy-host' "${repo_root}/Dockerfile"

# Confirm the default profile does not contain the host system bus bind mount.
if grep -Fq '/run/dbus/system_bus_socket' "$compose"; then
  echo "Default Compose profile must not mount the host system bus." >&2
  exit 1
fi

echo "Docker deployment static checks passed. Live Docker/D-Bus/Polkit integration remains required."
