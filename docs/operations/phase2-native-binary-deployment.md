# Machinarchy Agent — Phase 2: Native Linux Binary Deployment

**Status:** Implementation package prepared; production installation validation remains outstanding.

## Summary

Phase 2 completes the Agent's native Linux binary deployment path without adding OS-management capabilities or modifying the controller.

### Implemented

1. **Versioned release packages** for Linux AMD64 and ARM64. Each archive contains the binary, systemd unit, production environment template, install/rollback/uninstall scripts, installation guide, license and build metadata.
2. **Release metadata** embedded into each binary: version tag, commit SHA and build timestamp.
3. **SHA-256 checksums** for both release archives.
4. **Safer systemd installation and upgrades:** dedicated `machagent` system user/group with dynamically allocated IDs, non-interactive shell, host-architecture check for release archives, correct configuration directory group permissions, atomic binary/unit replacement, and preservation of existing configuration and state.
5. **One-step rollback** by swapping the current and previous binary/unit, without changing Agent identity or durable operation state.
6. **Non-destructive uninstall** by default. Permanent deletion of configuration, credentials and state requires `--purge` and confirmation (or `--yes` for automation). Uninstall refuses to remove the executable if an active service cannot be stopped.
7. **Configuration preflight:** `machinarchy-agent --check-config` validates the environment without starting the runtime or connecting to NATS. The systemd guide runs this check as the unprivileged Agent user using a transient unit.
8. **CI packaging verification:** shell syntax checks, archive structure/checksum smoke tests and systemd unit verification in a temporary root.

### Main operator workflow

Download and verify a versioned release archive, extract it, install the systemd service, configure the environment, validate the configuration, then enable the service. The full commands and upgrade/uninstall details are in `machinarchy-agent/docs/deployment/systemd.md`.

### Verification performed

- `make fmt-check` — passed.
- `make deployment-check` — passed: shell syntax, AMD64/ARM64 package smoke tests, archive metadata, SHA-256 validation and `systemd-analyze verify` in an isolated temporary root.
- `scripts/verify-agent-isolated.sh` — passed: shared protocol tests, Agent unit tests, race detector, `go vet`, build, `--version`, `--check-config` and Windows cross-compilation.
- The race suite failed once on an existing timing-sensitive cancellation-result test, then passed on immediate rerun and on the final full run. The dispatcher itself was not changed in this phase.

### Important limitations

- The repository declares Go 1.26.0, but the required toolchain could not be downloaded in this environment. Isolated verification used Go 1.23.2 with a compile-time NATS API stub and a local protocol checkout. This does not prove compatibility with the real NATS v1.55.0 client or live JetStream.
- The GitHub Release workflow was updated but not executed against GitHub. No real tagged release assets were published.
- Install, upgrade, rollback and uninstall scripts were syntax-checked and their package behavior tested, but were not executed against a live Debian/Ubuntu/ARM64 systemd VM.
- Real Polkit/systemd service-control permissions remain unverified.
- SHA-256 assets are published alongside release archives; this is transfer-integrity checking, not an independent publisher signature or provenance attestation.
- Docker host OS management remains out of scope for this phase. The existing container image is not approved for managing host services.

This package is based on the Phase 1 source archive available in this workspace. It was not committed directly to the user's GitHub repository. Review and merge it into the current repository before tagging a release.
