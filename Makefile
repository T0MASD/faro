# Faro Makefile

.PHONY: help build build-dev test test-ci test-unit test-e2e test-integration clean tag-patch tag-minor tag-major

# Default target
help:
	@echo "Faro - Kubernetes Resource Monitor"
	@echo ""
	@echo "Available targets:"
	@echo "  build            - Build the faro binary"
	@echo "  build-dev        - Build with development version info"
	@echo "  test             - Run all tests (unit + e2e + integration) - requires K8s"
	@echo "  test-ci          - Run CI-safe tests only (unit tests, no K8s required)"
	@echo "  test-unit        - Run unit tests only (no K8s required)"
	@echo "  test-e2e         - Run E2E tests only (requires K8s cluster)"
	@echo "  test-integration - Run integration tests only (requires K8s cluster)"
	@echo "  clean            - Clean build artifacts and test logs"
	@echo "  tag-patch        - Create patch version tag and trigger release"
	@echo "  tag-minor        - Create minor version tag and trigger release"
	@echo "  tag-major        - Create major version tag and trigger release"
	@echo "  help             - Show this help message"

# Build the faro binary
build:
	@echo "Building faro binary..."
	go build -o faro main.go

# Build with version information (for local testing)
build-dev:
	@echo "Building faro with dev version info..."
	go build -ldflags "-X main.version=dev-$(shell git rev-parse --short HEAD) -X main.commit=$(shell git rev-parse HEAD) -X main.date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ) -X main.builtBy=make" -o faro main.go

# Run all tests (requires Kubernetes cluster)
test: clean test-unit test-e2e test-integration

# Run only tests that don't require Kubernetes (for CI/releases)
test-ci: test-unit
	@echo "CI tests completed (unit tests only)"

# Run unit tests
test-unit:
	@echo "Running unit tests..."
	cd tests/unit && go test -v

# Run E2E tests
test-e2e:
	@echo "Running E2E tests in parallel..."
	@echo "Note: Requires access to a Kubernetes cluster"
	cd tests/e2e && go test -v -parallel 9 -timeout 10m

# Run integration tests
test-integration:
	@echo "Running integration tests in parallel..."
	@echo "Note: Requires access to a Kubernetes cluster"
	cd tests/integration && go test -v -parallel 3 -timeout 5m

# Clean build artifacts and test logs
clean:
	@echo "Cleaning up..."
	rm -f faro workload-monitor
	find tests -type d -name "logs" -exec rm -rf {} + 2>/dev/null || true
	@echo "Clean complete"

# Git tag helpers (triggers GitHub Actions release)
tag-patch:
	@echo "Creating patch version tag (triggers GitHub Actions release)..."
	@./scripts/tag-version.sh patch

tag-minor:
	@echo "Creating minor version tag (triggers GitHub Actions release)..."
	@./scripts/tag-version.sh minor

tag-major:
	@echo "Creating major version tag (triggers GitHub Actions release)..."
	@./scripts/tag-version.sh major