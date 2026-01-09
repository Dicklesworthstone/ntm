# NTM - Named Tmux Manager
# https://github.com/Dicklesworthstone/ntm

BINARY_NAME := ntm
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-s -w -X github.com/Dicklesworthstone/ntm/internal/cli.Version=$(VERSION)"

GO := go
GOFLAGS := -trimpath

# Output directory
DIST := dist

.PHONY: all build clean install test lint fmt help pre-commit upgrade-contract

all: build

## Build for current platform
build:
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME) ./cmd/ntm

## Build for all platforms
build-all: clean
	@mkdir -p $(DIST)
	GOOS=darwin  GOARCH=amd64 $(GO) build $(GOFLAGS) $(LDFLAGS) -o $(DIST)/$(BINARY_NAME)-darwin-amd64 ./cmd/ntm
	GOOS=darwin  GOARCH=arm64 $(GO) build $(GOFLAGS) $(LDFLAGS) -o $(DIST)/$(BINARY_NAME)-darwin-arm64 ./cmd/ntm
	GOOS=linux   GOARCH=amd64 $(GO) build $(GOFLAGS) $(LDFLAGS) -o $(DIST)/$(BINARY_NAME)-linux-amd64 ./cmd/ntm
	GOOS=linux   GOARCH=arm64 $(GO) build $(GOFLAGS) $(LDFLAGS) -o $(DIST)/$(BINARY_NAME)-linux-arm64 ./cmd/ntm
	@echo "Built binaries in $(DIST)/"

## Install to /usr/local/bin
install: build
	install -m 755 $(BINARY_NAME) /usr/local/bin/$(BINARY_NAME)
	@echo "Installed $(BINARY_NAME) to /usr/local/bin/"
	@echo ""
	@echo "Add to your shell rc file:"
	@echo '  eval "$$(ntm init zsh)"   # for zsh'
	@echo '  eval "$$(ntm init bash)"  # for bash'

## Install to user bin directory
install-user: build
	@mkdir -p $(HOME)/.local/bin
	install -m 755 $(BINARY_NAME) $(HOME)/.local/bin/$(BINARY_NAME)
	@echo "Installed $(BINARY_NAME) to ~/.local/bin/"
	@echo "Make sure ~/.local/bin is in your PATH"

## Uninstall
uninstall:
	rm -f /usr/local/bin/$(BINARY_NAME)
	rm -f $(HOME)/.local/bin/$(BINARY_NAME)
	@echo "Uninstalled $(BINARY_NAME)"

## Run tests
test:
	$(GO) test -v ./...

## Validate upgrade asset naming contract
upgrade-contract:
	@echo "Running upgrade contract tests..."
	$(GO) test -v -run TestUpgradeAsset ./internal/cli/

## Pre-commit checks (run upgrade contract tests when relevant files are staged)
pre-commit:
	@changed=$$(git diff --cached --name-only); \
	if echo "$$changed" | grep -Eq '(^|/)(\.goreleaser\.yaml|internal/cli/upgrade.go|internal/cli/cli_test.go)$$'; then \
		echo "Detected upgrade-related staged changes; running upgrade contract tests..."; \
		$(GO) test -v -run TestUpgradeAsset ./internal/cli/; \
	else \
		echo "No upgrade-related staged changes detected; skipping upgrade contract tests."; \
	fi

## Run tests with coverage
test-coverage:
	$(GO) test -v -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## Lint the code
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed. Run: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

## Format code
fmt:
	$(GO) fmt ./...
	@if command -v goimports >/dev/null 2>&1; then \
		goimports -w .; \
	fi

## Clean build artifacts
clean:
	rm -f $(BINARY_NAME)
	rm -rf $(DIST)
	rm -f coverage.out coverage.html

## Update dependencies
deps:
	$(GO) mod download
	$(GO) mod tidy

## Generate completions
completions:
	@mkdir -p $(DIST)/completions
	./$(BINARY_NAME) completion bash > $(DIST)/completions/ntm.bash
	./$(BINARY_NAME) completion zsh > $(DIST)/completions/_ntm
	./$(BINARY_NAME) completion fish > $(DIST)/completions/ntm.fish
	@echo "Generated completions in $(DIST)/completions/"

## Show version
version:
	@echo $(VERSION)

## Show help
help:
	@echo "NTM - Named Tmux Manager"
	@echo ""
	@echo "Usage: make <target>"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
	@echo ""
	@echo "Build targets:"
	@echo "  build       Build for current platform"
	@echo "  build-all   Build for all platforms"
	@echo "  install     Install to /usr/local/bin"
	@echo "  install-user Install to ~/.local/bin"
	@echo ""
	@echo "Development:"
	@echo "  test        Run tests"
	@echo "  lint        Run linter"
	@echo "  fmt         Format code"
	@echo "  clean       Remove build artifacts"
