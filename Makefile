SHELL := /usr/bin/env bash
GO ?= go
BIN_DIR ?= bin
BIN_NAME := machinarchy-agent
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildTime=$(BUILD_TIME)

.PHONY: help fmt fmt-check test test-race vet build run check verify-isolated deployment-check clean docker-build

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"}; /^[a-zA-Z0-9_-]+:.*##/ {printf "%-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format Go sources
	$(GO) fmt ./...

fmt-check: ## Fail when Go sources are not gofmt formatted
	@test -z "$$($(GO)fmt -l $$(find cmd internal -name '*.go' -type f))" || { \
		echo "Go files need gofmt. Run make fmt." >&2; exit 1; }

test: ## Run unit tests
	$(GO) test -count=1 ./...

test-race: ## Run race-enabled tests
	$(GO) test -race -count=1 ./...

vet: ## Run go vet
	$(GO) vet ./...

build: ## Build the agent binary
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BIN_NAME) ./cmd/machinarchy-agent

run: ## Run the agent with environment inherited from the shell
	$(GO) run ./cmd/machinarchy-agent

check: fmt-check test test-race vet build deployment-check ## Run all local quality checks

verify-isolated: ## Verify agent source using the isolated NATS API stub harness
	./scripts/verify-agent-isolated.sh

deployment-check: ## Validate deployment scripts and release archive packaging
	@for script in scripts/install-systemd.sh scripts/uninstall-systemd.sh scripts/rollback-systemd.sh scripts/package-release.sh scripts/test-release-package.sh scripts/verify-systemd-unit.sh; do bash -n "$$script"; done
	./scripts/test-release-package.sh
	./scripts/verify-systemd-unit.sh

docker-build: ## Build local container image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t machinarchy-agent:$(VERSION) .

clean: ## Remove local build and test artifacts
	rm -rf $(BIN_DIR) coverage.out coverage.html
