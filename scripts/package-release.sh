#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: $0 VERSION COMMIT BUILD_TIME [DIST_DIR]" >&2
  echo "Expected built binaries: DIST_DIR/machinarchy-agent-linux-amd64 and -arm64" >&2
}

if (($# < 3 || $# > 4)); then
  usage
  exit 2
fi

version="$1"
commit="$2"
build_time="$3"
dist_dir="${4:-dist}"

[[ "${version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]] || {
  echo "Invalid release version/tag: ${version}" >&2
  exit 2
}
[[ -n "${commit}" && "${commit}" != *$'\n'* ]] || { echo "Invalid commit value" >&2; exit 2; }
[[ -n "${build_time}" && "${build_time}" != *$'\n'* ]] || { echo "Invalid build time" >&2; exit 2; }

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p "${dist_dir}"
dist_dir="$(cd -- "${dist_dir}" && pwd)"

for arch in amd64 arm64; do
  binary="${dist_dir}/machinarchy-agent-linux-${arch}"
  [[ -f "${binary}" && -x "${binary}" ]] || {
    echo "Missing executable release binary: ${binary}" >&2
    exit 1
  }

  package_name="machinarchy-agent_${version}_linux_${arch}"
  stage_root="$(mktemp -d "${dist_dir}/.package-${arch}.XXXXXX")"
  stage_dir="${stage_root}/${package_name}"
  mkdir -p "${stage_dir}"
  cleanup_stage() { rm -rf -- "${stage_root}"; }
  trap cleanup_stage EXIT

  install -m 0755 "${binary}" "${stage_dir}/machinarchy-agent"
  install -m 0644 "${repo_root}/deployments/systemd/machinarchy-agent.service" "${stage_dir}/machinarchy-agent.service"
  install -m 0644 "${repo_root}/deployments/systemd/agent.env.example" "${stage_dir}/agent.env.example"
  install -m 0755 "${repo_root}/scripts/install-systemd.sh" "${stage_dir}/install-systemd.sh"
  install -m 0755 "${repo_root}/scripts/uninstall-systemd.sh" "${stage_dir}/uninstall-systemd.sh"
  install -m 0755 "${repo_root}/scripts/rollback-systemd.sh" "${stage_dir}/rollback-systemd.sh"
  install -m 0644 "${repo_root}/docs/deployment/systemd.md" "${stage_dir}/INSTALL.md"
  install -m 0644 "${repo_root}/LICENSE" "${stage_dir}/LICENSE"
  {
    printf 'version=%s\n' "${version}"
    printf 'commit=%s\n' "${commit}"
    printf 'build_time=%s\n' "${build_time}"
    printf 'os=linux\n'
    printf 'arch=%s\n' "${arch}"
  } > "${stage_dir}/BUILD-INFO.txt"

  tar -czf "${dist_dir}/${package_name}.tar.gz" -C "${stage_root}" "${package_name}"
  cleanup_stage
  trap - EXIT
done

(
  cd "${dist_dir}"
  sha256sum "machinarchy-agent_${version}_linux_amd64.tar.gz" \
    "machinarchy-agent_${version}_linux_arm64.tar.gz" > checksums.txt
)

echo "Created Linux AMD64/ARM64 release archives and checksums in ${dist_dir}."
