SHELL := /usr/bin/env bash
GO ?= go
BIN_DIR ?= bin
BIN_NAME := machinarchy-agent
PROTOCOL_DIR := protocol
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildTime=$(BUILD_TIME)
GO_FILES := $(shell find cmd internal protocol -name '*.go' -type f)

.PHONY: help fmt fmt-check test test-race vet build run check verify-isolated deployment-check protocol-test protocol-test-race protocol-vet clean docker-build

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"}; /^[a-zA-Z0-9_-]+:.*##/ {printf "%-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format Agent and protocol Go sources
	$(GO) fmt ./...
	cd $(PROTOCOL_DIR) && $(GO) fmt ./...

fmt-check: ## Fail when Agent or protocol Go sources are not gofmt formatted
	@test -z "$$($(GO)fmt -l $(GO_FILES))" || { \
		echo "Go files need gofmt. Run make fmt." >&2; exit 1; }

test: protocol-test ## Run unit tests for the Agent and protocol modules
	$(GO) test -count=1 ./...

protocol-test: ## Run shared protocol module unit tests
	cd $(PROTOCOL_DIR) && $(GO) test -count=1 ./...

test-race: protocol-test-race ## Run race-enabled tests for both modules
	$(GO) test -race -count=1 ./...

protocol-test-race: ## Run protocol module race-enabled tests
	cd $(PROTOCOL_DIR) && $(GO) test -race -count=1 ./...

vet: protocol-vet ## Run go vet for the Agent and protocol modules
	$(GO) vet ./...

protocol-vet: ## Run go vet for the protocol module
	cd $(PROTOCOL_DIR) && $(GO) vet ./...

build: ## Build the agent binary
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BIN_NAME) ./cmd/machinarchy-agent

run: ## Run the agent with environment inherited from the shell
	$(GO) run ./cmd/machinarchy-agent

check: fmt-check test test-race vet build deployment-check ## Run all local quality checks

verify-isolated: ## Verify Agent and bundled protocol with the isolated NATS API stub harness
	./scripts/verify-agent-isolated.sh

deployment-check: ## Validate deployment scripts and release archive packaging
	@for script in scripts/install-systemd.sh scripts/uninstall-systemd.sh scripts/rollback-systemd.sh scripts/package-release.sh scripts/test-release-package.sh scripts/verify-systemd-unit.sh scripts/prepare-docker-host.sh scripts/remove-docker-host-policy.sh scripts/lib/docker-host.sh scripts/test-docker-deployment.sh; do bash -n "$$script"; done
	./scripts/test-release-package.sh
	./scripts/verify-systemd-unit.sh
	./scripts/test-docker-deployment.sh

docker-build: ## Build local container image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t machinarchy-agent:$(VERSION) .

clean: ## Remove local build and test artifacts
	rm -rf $(BIN_DIR) dist coverage.out coverage.html
