# Agent Release Process

1. Confirm the controller supports the protocol module version pinned by the
   Agent. Do not tag a release that is incompatible with the controller.
2. Run CI, race tests, vet, deployment packaging checks, real NATS integration
   and controller/agent end-to-end tests.
3. Review `CHANGELOG.md`, `ChangeLogAI.md` and the compatibility matrix.
4. Create and push a signed annotated tag (`vX.Y.Z`).
5. The release workflow builds Linux AMD64 and ARM64 binaries with release
   metadata, packages each with its systemd unit, environment template,
   installer, rollback and uninstall helpers, and publishes SHA-256 checksums.
6. Verify the published assets and checksums before deploying to a canary host.
7. Roll out to a canary host first. Preserve Agent identity and operation state
   during upgrades and rollbacks.
8. Confirm Agent lease health and operation result persistence in the
   controller before wider rollout.

The current Docker image workflow is separate from native binary releases.
Publishing a container image does not make host OS management through Docker a
supported deployment; host integration remains a separate implementation and
verification phase.
