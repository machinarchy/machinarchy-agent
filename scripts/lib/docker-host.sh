#!/usr/bin/env bash

# Select the least-privileged container group that can connect to the host
# system bus. For a world-accessible socket, do not add a host supplemental
# group: the Agent's primary container group is sufficient. Otherwise require
# a non-root socket group with read/write permissions.
machinarchy_dbus_access_gid() {
  local socket_gid="${1:-}"
  local socket_mode="${2:-}"
  local mode_value

  [[ "$socket_gid" =~ ^[0-9]+$ ]] || return 1
  [[ "$socket_mode" =~ ^[0-7]{3,4}$ ]] || return 1
  mode_value=$((8#$socket_mode))

  if (( (mode_value & 0006) == 0006 )); then
    printf '10001\n'
    return 0
  fi

  if [[ "$socket_gid" != "0" ]] && (( (mode_value & 0060) == 0060 )); then
    printf '%s\n' "$socket_gid"
    return 0
  fi

  return 1
}
