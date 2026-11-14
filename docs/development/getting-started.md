# Development Setup

## Prerequisites

- Go 1.26+
- Git
- NATS Server for transport integration tests
- The `machinarchy-protocol` repository at a sibling path for local workspace
  development

## Local workspace

```bash
mkdir -p ~/src/machinarchy
cd ~/src/machinarchy
git clone git@github.com:machinarchy/machinarchy-agent.git
git clone git@github.com:machinarchy/machinarchy-protocol.git
go work init ./machinarchy-agent ./machinarchy-protocol
cd machinarchy-agent
go mod tidy
go mod download
make check
```

The Go workspace selects the checked-out local protocol module. Without a
workspace, the agent resolves the protocol version pinned in `go.mod` from the
module proxy or Git. Commit the resulting `go.sum`; it is part of the dependency
integrity lock for this application. For a private protocol repository, set
`GOPRIVATE=github.com/machinarchy/machinarchy-protocol` and configure a
read-only GitHub credential. In this repository's GitHub Actions secrets, set
`MACHINARCHY_PROTOCOL_READ_TOKEN` to a fine-grained token (or GitHub App token)
with read-only contents access to the protocol repository. The workflow uses
it only for module download; do not grant write access.

## Run

The runtime needs a live controller implementing the registration and lease
request/reply contract. Starting only a NATS server will not make the agent
ready. Configure the NATS endpoint and an enrollment token, then run:

```bash
cp .env.example .env
chmod 600 .env
set -a; . ./.env; set +a
go run ./cmd/machinarchy-agent
```

## Checks

```bash
make fmt
make fmt-check
make test
make test-race
make vet
make build
make verify-isolated
```
