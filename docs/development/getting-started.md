# Development Setup

## Prerequisites

- Go 1.26+
- Git
- NATS Server for transport integration tests

## Clone and verify

```bash
git clone git@github.com:machinarchy/machinarchy-agent.git
cd machinarchy-agent
go mod download
make check
make verify-isolated
```

The shared protocol contracts are included in `protocol/` as a nested Go
module. The Agent root module uses a local `replace` directive to test against
that source. No sibling checkout, `go.work` file, private protocol repository
credential, or `GOPRIVATE` setting is required for the protocol module.

The protocol module remains independently consumable by the controller. Its
module path is `github.com/machinarchy/machinarchy-agent/protocol`; publish
protocol releases with `protocol/vX.Y.Z` Git tags. See
[`../protocol/compatibility.md`](../protocol/compatibility.md) for the
controller migration and compatibility requirements.

## Run

The runtime needs a live controller implementing the registration and lease
request/reply contract. Starting only a NATS server will not make the Agent
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
make check
```

These root targets include the nested protocol module where appropriate.
