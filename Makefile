.PHONY: build install test lint tidy clean fmt fmt-check spell secrets-scan check

BIN_DIR := cmd/bin
BINARY := $(BIN_DIR)/agentctl
GOPATH_BIN := $(shell go env GOPATH)/bin
# Embedded in agentctl -version (git describe, or "dev" outside a repo)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

# Build the cmd binary into cmd/bin (default: `make` with no target runs build)
build:
	@echo "==> Building..."
	@mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BINARY) ./cmd
	@echo "==> Build complete: $(BINARY)"

# Install agentctl to $(GOPATH)/bin (run agentctl from anywhere if that dir is in PATH)
install: build
	@mkdir -p $(GOPATH_BIN)
	cp $(BINARY) $(GOPATH_BIN)/agentctl
	@echo "Installed to $(GOPATH_BIN)/agentctl"

# Run tests under pkg
test:
	@echo "==> Running tests..."
	go test ./pkg/... -count=1
	go test ./internal/... -count=1
	@echo "==> Tests complete"

# Full gate: format, then tests + fmt-check + spell + go vet + golangci-lint + secrets-scan (lint runs fmt-check and spell)
check: fmt test lint secrets-scan
	@echo "==> All checks passed"

# Run tests with coverage
test-coverage:
	@echo "==> Running tests with coverage..."
	go test ./... -count=1 -coverprofile=coverage.out
	@echo "==> Total coverage:"
	@go tool cover -func=coverage.out | grep '^total:'
	go tool cover -html=coverage.out -o coverage.html
	@echo "==> Coverage report: coverage.html"

# Format all Go files (gofmt with simplify; same as Go Report Card / many style checks)
fmt:
	@echo "==> gofmt -s -w"
	gofmt -s -w .
	@echo "==> Format complete"

# Fail if any .go file needs gofmt -s (run `make fmt` to fix)
fmt-check:
	@echo "==> Checking gofmt -s..."
	@files=$$(gofmt -s -l .); \
	if [ -n "$$files" ]; then \
		echo "These files are not gofmt -s formatted. Run: make fmt"; \
		echo "$$files"; \
		exit 1; \
	fi
	@echo "==> gofmt -s OK"

# Typos in source (same family of checks as Go Report Card "misspell"; no extra install — uses go run)
spell:
	@echo "==> misspell"
	go run github.com/client9/misspell/cmd/misspell@latest -error .

# Run linters (gofmt -s, misspell, go vet + golangci-lint). golangci-lint must be built with Go >= go.mod (see CONTRIBUTING.md).
lint: fmt-check spell
	@echo "==> Checking lints (go vet + golangci-lint)..."
	go vet ./...
	golangci-lint run ./...
	@echo "==> Lint complete"

# Tidy module dependencies
tidy:
	@echo "==> Tidying module dependencies..."
	go mod tidy
	@echo "==> Tidy complete"

# Remove built binaries and generated artifacts
clean:
	@echo "==> Cleaning..."
	rm -rf $(BIN_DIR)
	rm -f coverage.out coverage.html
	@echo "==> Clean complete"

# Scan tracked files for leaked secrets (install: https://github.com/gitleaks/gitleaks#installing)
secrets-scan:
	@echo "==> gitleaks detect"
	@if command -v gitleaks >/dev/null 2>&1; then \
		gitleaks detect --source . --verbose --redact; \
	elif command -v docker >/dev/null 2>&1; then \
		docker run --rm -v "$$(pwd):/repo" -w /repo zricethezav/gitleaks:latest detect --source=/repo --verbose --redact; \
	else \
		echo "Install gitleaks or Docker."; \
		exit 1; \
	fi
