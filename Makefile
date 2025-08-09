# Faro Makefile

# Variables
BINARY_NAME=faro
PACKAGE=.
GO_FILES=$(shell find . -name "*.go" -type f)

# Default target
.PHONY: all
all: build

# Build the binary
.PHONY: build
build:
	go build -o $(BINARY_NAME) $(PACKAGE)

# Build with optimizations for production
.PHONY: build-prod
build-prod:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY_NAME) $(PACKAGE)

# Run the application
.PHONY: run
run: build
	./$(BINARY_NAME)

# Run with config file
.PHONY: run-config
run-config: build
	./$(BINARY_NAME) --config=examples/minimal-config.yaml

# Clean build artifacts
.PHONY: clean
clean:
	rm -f $(BINARY_NAME)
	rm -rf output/
	rm -rf logs/

# Run tests
.PHONY: test
test:
	go test ./...

# Run E2E tests
.PHONY: test-e2e
test-e2e: build
	cd e2e && ./test1.sh && ./test2.sh && ./test3.sh && ./test4.sh && ./test5.sh && ./test6.sh && ./test7.sh

# Format code
.PHONY: fmt
fmt:
	go fmt ./...

# Lint code
.PHONY: lint
lint:
	golangci-lint run

# Tidy dependencies
.PHONY: tidy
tidy:
	go mod tidy

# Install dependencies
.PHONY: deps
deps:
	go mod download

# Development setup
.PHONY: dev-setup
dev-setup: deps fmt tidy

# Help target
.PHONY: help
help:
	@echo "Available targets:"
	@echo "  build      - Build the faro binary"
	@echo "  build-prod - Build optimized binary for production"
	@echo "  run        - Build and run faro"
	@echo "  run-config - Build and run faro with example config"
	@echo "  clean      - Remove build artifacts and output files"
	@echo "  test       - Run unit tests"
	@echo "  test-e2e   - Run end-to-end tests"
	@echo "  fmt        - Format Go code"
	@echo "  lint       - Run linter"
	@echo "  tidy       - Tidy Go modules"
	@echo "  deps       - Download dependencies"
	@echo "  dev-setup  - Setup development environment"
	@echo "  help       - Show this help message"