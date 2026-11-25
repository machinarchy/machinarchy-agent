#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf -- "${tmp_dir}"' EXIT
mkdir -p "${tmp_dir}/dist"

# Packaging is tested with inert executable fixtures; no Go toolchain or
# controller connection is needed to validate release archive structure.
for arch in amd64 arm64; do
  printf '#!/bin/sh\necho fixture-%s\n' "${arch}" > "${tmp_dir}/dist/machinarchy-agent-linux-${arch}"
  chmod 0755 "${tmp_dir}/dist/machinarchy-agent-linux-${arch}"
done

"${repo_root}/scripts/package-release.sh" v9.8.7 deadbeef 2026-11-14T00:00:00Z "${tmp_dir}/dist"

for arch in amd64 arm64; do
  archive="${tmp_dir}/dist/machinarchy-agent_v9.8.7_linux_${arch}.tar.gz"
  [[ -s "${archive}" ]]
  listing="$(tar -tzf "${archive}")"
  for required in \
    "machinarchy-agent_v9.8.7_linux_${arch}/machinarchy-agent" \
    "machinarchy-agent_v9.8.7_linux_${arch}/machinarchy-agent.service" \
    "machinarchy-agent_v9.8.7_linux_${arch}/agent.env.example" \
    "machinarchy-agent_v9.8.7_linux_${arch}/install-systemd.sh" \
    "machinarchy-agent_v9.8.7_linux_${arch}/uninstall-systemd.sh" \
    "machinarchy-agent_v9.8.7_linux_${arch}/rollback-systemd.sh" \
    "machinarchy-agent_v9.8.7_linux_${arch}/BUILD-INFO.txt" \
    "machinarchy-agent_v9.8.7_linux_${arch}/LICENSE"; do
    grep -Fxq "${required}" <<< "${listing}" || {
      echo "Archive is missing ${required}" >&2
      exit 1
    }
  done
  tar -xOf "${archive}" "machinarchy-agent_v9.8.7_linux_${arch}/machinarchy-agent" | grep -q "fixture-${arch}"
  tar -xOf "${archive}" "machinarchy-agent_v9.8.7_linux_${arch}/BUILD-INFO.txt" | grep -Fxq "arch=${arch}"
  extract_dir="${tmp_dir}/extracted-${arch}"
  mkdir -p "${extract_dir}"
  tar -xzf "${archive}" -C "${extract_dir}"
  "${extract_dir}/machinarchy-agent_v9.8.7_linux_${arch}/install-systemd.sh" --help >/dev/null
  "${extract_dir}/machinarchy-agent_v9.8.7_linux_${arch}/uninstall-systemd.sh" --help >/dev/null
done

(
  cd "${tmp_dir}/dist"
  sha256sum --check checksums.txt
  [[ "$(wc -l < checksums.txt)" -eq 2 ]]
)

for script in install-systemd.sh uninstall-systemd.sh rollback-systemd.sh package-release.sh; do
  bash -n "${repo_root}/scripts/${script}"
done

echo "Release packaging smoke tests passed."
