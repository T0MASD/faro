# Faro Makefile

.PHONY: help build build-all build-dev test test-ci test-pkg test-unit test-e2e test-integration test-operator clean tag-patch tag-minor tag-major operator-image operator-image-load

# Default target
help:
	@echo "Faro - Kubernetes Resource Monitor"
	@echo ""
	@echo "Available targets:"
	@echo "  build            - Build the faro binary"
	@echo "  build-all        - Compile every package, examples included"
	@echo "  build-dev        - Build with development version info"
	@echo "  test             - Run all tests (unit + e2e + integration) - requires K8s"
	@echo "  test-ci          - Run CI-safe tests only (pkg + unit, no K8s required)"
	@echo "  test-pkg         - Run in-package tests only (no K8s required)"
	@echo "  test-unit        - Run unit tests only (no K8s required)"
	@echo "  test-e2e         - Run E2E tests only (requires K8s cluster)"
	@echo "  test-integration - Run integration tests only (requires K8s cluster)"
	@echo "  test-operator    - Run operator deployment tests (requires kinc cluster)"
	@echo "  clean            - Clean build artifacts and test logs"
	@echo "  tag-patch        - Create patch version tag and trigger release"
	@echo "  tag-minor        - Create minor version tag and trigger release"
	@echo "  tag-major        - Create major version tag and trigger release"
	@echo ""
	@echo "Operator targets:"
	@echo "  operator-image      - Build faro operator container image"
	@echo "  operator-image-load - Load operator image into kinc cluster"
	@echo ""
	@echo "  help             - Show this help message"

# Build the faro binary
build:
	@echo "Building faro binary..."
	go build -o faro main.go

# Compile every package, examples included. `go build -o faro main.go` compiles
# one file, so an example that stopped matching the library still "built".
build-all:
	@echo "Building all packages..."
	go build ./...

# Build with version information (for local testing)
build-dev:
	@echo "Building faro with dev version info..."
	go build -ldflags "-X main.version=dev-$(shell git rev-parse --short HEAD) -X main.commit=$(shell git rev-parse HEAD) -X main.date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ) -X main.builtBy=make" -o faro main.go

# Run all tests (requires Kubernetes cluster)
test: clean test-pkg test-unit test-e2e test-integration

# Run only tests that don't require Kubernetes (for CI/releases)
test-ci: test-pkg test-unit
	@echo "CI tests completed (no cluster required)"

# Run in-package tests (root module)
#
# Separate from test-unit because they are a different module and test a
# different surface: tests/unit is an external package and can only reach
# exported API, while some behaviour is only reachable from inside pkg.
test-pkg:
	@echo "Running in-package tests..."
	go test -v ./pkg/...

# Run unit tests (tests/unit module, external package)
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

# Run operator deployment tests
test-operator:
	@echo "Running operator deployment validation tests..."
	@echo "Note: Requires kinc cluster running"
	bash scripts/test-operator.sh

# Clean build artifacts and test logs
#
# The binaries are every one a target here can produce: the faro binary, the
# e2e helper, and one per example, since `go build ./examples/<name>/` drops
# its binary in the working directory. The log trees are what a run writes:
# the tests write under tests/*/logs, and the examples take OutputDir ./logs,
# which lands wherever they were run from.
clean:
	@echo "Cleaning up..."
	rm -f faro faro-e2e test_import
	rm -f library-usage worker-dispatcher workload-monitor
	rm -rf logs examples/logs
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

# Operator container image targets
OPERATOR_IMAGE ?= localhost/faro-operator
OPERATOR_TAG ?= latest

# OPERATOR_IMAGE is a repository, and OPERATOR_TAG is appended to it. Passing a
# reference that already carries a tag yields name:tag:tag, which podman rejects
# as an invalid reference after the build has already run.
operator-image:
	@echo "Building faro operator container image..."
	@ref='$(OPERATOR_IMAGE)'; \
	case "$${ref##*/}" in \
	  *:*) echo "❌ OPERATOR_IMAGE must be a repository without a tag."; \
	       echo "   Got: $$ref"; \
	       echo "   Use: make operator-image OPERATOR_IMAGE=$${ref%:*} OPERATOR_TAG=$${ref##*:}"; \
	       exit 1 ;; \
	esac
	@echo "Image: $(OPERATOR_IMAGE):$(OPERATOR_TAG)"
	podman build -f Dockerfile -t $(OPERATOR_IMAGE):$(OPERATOR_TAG) .
	@echo "✅ Operator image built: $(OPERATOR_IMAGE):$(OPERATOR_TAG)"
	@echo "Note: Image is available to kinc via shared container storage"

operator-image-load:
	@echo "Loading operator image into kinc cluster..."
	@echo "Note: kinc uses shared container storage - image should be automatically available"
	@echo "Verifying image exists..."
	podman image exists $(OPERATOR_IMAGE):$(OPERATOR_TAG) && echo "✅ Image $(OPERATOR_IMAGE):$(OPERATOR_TAG) is available" || (echo "❌ Image not found. Run 'make operator-image' first" && exit 1)