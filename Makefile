SHELL := /usr/bin/env bash
GO ?= go
BIN_DIR ?= bin
BIN_NAME := machinarchy-agent
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
# protocol/ is its own module (imported by machinarchy); every Go target
# runs in both modules.
MODULES := . protocol
LDFLAGS := -s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildTime=$(BUILD_TIME)

.PHONY: help fmt fmt-check tidy-check test test-race coverage vet lint vuln gosec fuzz build run check deployment-check clean docker-build

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"}; /^[a-zA-Z0-9_-]+:.*##/ {printf "%-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format Go sources
	$(GO) fmt ./...

fmt-check: ## Fail when Go sources are not gofmt formatted
	@test -z "$$(gofmt -l cmd internal protocol)" || { gofmt -l cmd internal protocol; echo "Run make fmt." >&2; exit 1; }

tidy-check: ## Fail when go.mod/go.sum are not tidy or modules fail verification
	@for m in $(MODULES); do (cd $$m && $(GO) mod verify && $(GO) mod tidy -diff) || exit 1; done

test: ## Run unit and integration tests (embedded NATS server)
	@for m in $(MODULES); do (cd $$m && $(GO) test -count=1 ./...) || exit 1; done

test-race: ## Run all tests with the race detector
	@for m in $(MODULES); do (cd $$m && $(GO) test -race -count=1 ./...) || exit 1; done

coverage: ## Run tests with the race detector and write coverage.out
	$(GO) test -race -count=1 -covermode=atomic -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

vet: ## Run go vet for the host and Linux release targets
	@for m in $(MODULES); do (cd $$m && $(GO) vet ./... && GOOS=linux GOARCH=amd64 $(GO) vet ./... && GOOS=linux GOARCH=arm64 $(GO) vet ./...) || exit 1; done

lint: ## Run golangci-lint (.golangci.yml)
	@for m in $(MODULES); do (cd $$m && golangci-lint run --timeout=5m --config $(CURDIR)/.golangci.yml ./...) || exit 1; done

vuln: ## Run govulncheck
	@for m in $(MODULES); do (cd $$m && govulncheck ./...) || exit 1; done

gosec: ## Run gosec
	gosec -quiet ./... ./protocol/...

build: ## Build the agent binary
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BIN_NAME) ./cmd/machinarchy-agent

FUZZTIME ?= 30s
FUZZ_TARGETS := FuzzOpenCommand FuzzOpenRegisterRequest FuzzParsePublicKey FuzzDecodeStrict

fuzz: ## Run each protocol fuzz target for FUZZTIME (default 30s)
	@for target in $(FUZZ_TARGETS); do \
		(cd protocol && $(GO) test -run=NONE -fuzz="^$$target$$" -fuzztime=$(FUZZTIME) ./agentv1) || exit 1; \
	done

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
