BINARY_NAME := kubevirt-datamover-monitor
CMD_PATH := ./cmd/kubevirt-datamover-monitor
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.Version=$(VERSION)

# golangci-lint binary location
GOLANGCI_LINT ?= $(shell which golangci-lint 2>/dev/null)

.PHONY: build clean lint fmt vet test help cross-build golangci-lint ci

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

build: ## Build the monitor binary
	@mkdir -p bin
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY_NAME) $(CMD_PATH)

cross-build: ## Build for all supported platforms
	@mkdir -p bin
	GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY_NAME)-linux-amd64 $(CMD_PATH)
	GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY_NAME)-linux-arm64 $(CMD_PATH)
	GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY_NAME)-darwin-arm64 $(CMD_PATH)

clean: ## Remove build artifacts
	rm -rf bin/

# Install golangci-lint if not present
golangci-lint:
ifeq ($(GOLANGCI_LINT),)
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	$(eval GOLANGCI_LINT = $(shell go env GOPATH)/bin/golangci-lint)
endif

lint: golangci-lint ## Run golangci-lint
	$(GOLANGCI_LINT) run ./...

fmt: ## Run go fmt
	go fmt ./...

vet: ## Run go vet
	go vet ./...

test: ## Run tests with race detection
	go test -race ./...

ci: build test lint vet ## Run all checks
