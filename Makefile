SHELL := /usr/bin/env bash
GO ?= go
BIN_DIR ?= bin
BIN_NAME := machinarchy-agent
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildTime=$(BUILD_TIME)

.PHONY: help fmt fmt-check tidy-check test test-race vet lint vuln build run check deployment-check clean docker-build

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"}; /^[a-zA-Z0-9_-]+:.*##/ {printf "%-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format Go sources
	$(GO) fmt ./...

fmt-check: ## Fail when Go sources are not gofmt formatted
	@test -z "$$(gofmt -l cmd internal protocol)" || { gofmt -l cmd internal protocol; echo "Run make fmt." >&2; exit 1; }

tidy-check: ## Fail when go.mod/go.sum are not tidy or modules fail verification
	$(GO) mod verify
	$(GO) mod tidy -diff

test: ## Run unit and integration tests (embedded NATS server)
	$(GO) test -count=1 ./...

test-race: ## Run all tests with the race detector
	$(GO) test -race -count=1 ./...

vet: ## Run go vet for the host and Linux release targets
	$(GO) vet ./...
	GOOS=linux GOARCH=amd64 $(GO) vet ./...
	GOOS=linux GOARCH=arm64 $(GO) vet ./...

lint: ## Run golangci-lint (.golangci.yml)
	golangci-lint run --timeout=5m ./...

vuln: ## Run govulncheck
	govulncheck ./...

build: ## Build the agent binary
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BIN_NAME) ./cmd/machinarchy-agent

run: ## Run the agent with the environment inherited from the shell
	$(GO) run ./cmd/machinarchy-agent

check: fmt-check tidy-check vet test-race lint build deployment-check ## Run all local quality checks

deployment-check: ## Validate deployment scripts and release packaging
	@for script in scripts/*.sh scripts/lib/*.sh; do bash -n "$$script"; done
	./scripts/test-release-package.sh
	./scripts/verify-systemd-unit.sh
	./scripts/test-docker-deployment.sh

docker-build: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_TIME=$(BUILD_TIME) -t machinarchy-agent:$(VERSION) .

clean: ## Remove build and test artifacts
	rm -rf $(BIN_DIR) dist coverage.out coverage.html
