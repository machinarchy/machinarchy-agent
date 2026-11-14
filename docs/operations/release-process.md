# Agent Release Process

1. Confirm the controller supports the protocol module version pinned by the
   Agent.
2. Run CI, race tests, vet, NATS integration and controller/agent end-to-end tests.
3. Review `CHANGELOG.md`, `ChangeLogAI.md` and the compatibility matrix.
4. Create and push a signed annotated tag (`vX.Y.Z`).
5. The release workflow builds Linux AMD64 and ARM64 binaries, creates tarballs,
   computes SHA-256 checksums and publishes a GitHub Release.
6. The container workflow publishes an AMD64 GHCR image.
7. Roll out to a canary host before general deployment.
8. Confirm agent lease health and operation result persistence in the controller.
