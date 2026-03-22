.PHONY: build install test lint tidy clean

BIN_DIR := cmd/bin
BINARY := $(BIN_DIR)/agentctl
GOPATH_BIN := $(shell go env GOPATH)/bin

# Build the cmd binary into cmd/bin
build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BINARY) ./cmd

# Install agentctl to $(GOPATH)/bin (run agentctl from anywhere if that dir is in PATH)
install: build
	@mkdir -p $(GOPATH_BIN)
	cp $(BINARY) $(GOPATH_BIN)/agentctl
	@echo "Installed to $(GOPATH_BIN)/agentctl"

# Run tests under pkg
test:
	go test ./pkg/... -count=1

# Run tests with coverage
test-coverage:
	go test ./pkg/... -count=1 -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

# Run linters (go vet; golangci-lint if available)
lint:
	go vet ./...
	@which golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || true

# Tidy module dependencies
tidy:
	go mod tidy

# Remove built binaries and generated artifacts
clean:
	rm -rf $(BIN_DIR)
	rm -f coverage.out coverage.html
