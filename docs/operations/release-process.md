# Release Process

## Agent release

1. Confirm the controller supports the protocol module version pinned by the
   Agent and the required wire protocol version.
2. Run `make check` with the declared Go toolchain and real NATS dependency.
3. Build and publish the Agent with a root-level SemVer tag (`vX.Y.Z`).
4. Publish native Linux AMD64/ARM64 archives and checksums; the Docker workflow
   publishes matching multi-architecture images.

## Protocol module release

1. Review the JSON/wire compatibility impact and update contract tests.
2. Run `go test ./...`, `go test -race ./...` and `go vet ./...` from `protocol/`.
3. Coordinate the new module version with the controller and Agent maintainers.
4. Tag the nested module with the `protocol/vX.Y.Z` prefix (for example,
   `protocol/v0.1.0`).
5. Update the controller's `go.mod` and imports to the nested module path when
   a new protocol release is adopted.

Agent root tags and protocol-prefixed tags are independent version streams.
A protocol tag does not publish a new Agent binary/container release. A root
Agent tag does not publish a new protocol module version.

If the Agent repository is private, the controller CI/developer environment
needs read-only access to this repository to download the nested protocol
module. The Agent's own CI and Docker builds do not need a separate protocol
repository credential because the source is checked out with the Agent.
