#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
if ! command -v systemd-analyze >/dev/null 2>&1; then
  echo "systemd-analyze not installed; skipping systemd unit verification."
  exit 0
fi

tmp_root="$(mktemp -d)"
trap 'rm -rf -- "${tmp_root}"' EXIT
mkdir -p \
  "${tmp_root}/usr/local/bin" \
  "${tmp_root}/usr/lib/systemd/system" \
  "${tmp_root}/etc/systemd/system"

# Provide an inert executable and minimal target units so systemd-analyze can
# validate syntax/dependencies without requiring an Agent installation.
printf '#!/bin/sh\nexit 0\n' > "${tmp_root}/usr/local/bin/machinarchy-agent"
chmod 0755 "${tmp_root}/usr/local/bin/machinarchy-agent"
for target in sysinit.target network-online.target multi-user.target; do
  printf '[Unit]\nDescription=Test %s\n' "${target}" > "${tmp_root}/usr/lib/systemd/system/${target}"
done
cp "${repo_root}/deployments/systemd/machinarchy-agent.service" \
  "${tmp_root}/etc/systemd/system/machinarchy-agent.service"

systemd-analyze --root="${tmp_root}" verify machinarchy-agent.service
echo "systemd unit verification passed."
