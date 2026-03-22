.PHONY: build install test lint tidy clean

BIN_DIR := cmd/bin
BINARY := $(BIN_DIR)/agentctl
GOPATH_BIN := $(shell go env GOPATH)/bin

# Build the cmd binary into cmd/bin
build:
	@echo "==> Building..."
	@mkdir -p $(BIN_DIR)
	go build -o $(BINARY) ./cmd
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
	@echo "==> Tests complete"

# Run tests with coverage
test-coverage:
	@echo "==> Running tests with coverage..."
	go test ./pkg/... -count=1 -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@echo "==> Coverage report: coverage.html"

# Run linters (go vet + golangci-lint; requires golangci-lint: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
lint:
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
