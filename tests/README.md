# Faro Test Suite

This directory contains the complete test suite for the Faro Kubernetes resource monitor, organized into logical categories for maintainability and clarity.

## 📁 Directory Structure

```
tests/
├── unit/                    # Unit tests for library code
├── e2e/                     # End-to-end integration tests
├── integration/             # Integration tests using Faro library directly
└── archive/                 # Archived legacy tests
```

## 🧪 Test Categories

### Unit Tests (`unit/`)

**Purpose**: Test individual functions and components in isolation

**Coverage**:
- Configuration parsing and validation
- Data structure normalization
- Utility functions
- Error handling

**Usage**:
```bash
cd tests/unit
go test -v
```

**Files**:
- `config_test.go` - Tests for `pkg/config.go`
- `go.mod` - Unit test dependencies

### End-to-End Tests (`e2e/`)

**Purpose**: Test complete Faro functionality against a real Kubernetes cluster using the Faro binary

**Coverage**:
- Namespace-centric monitoring
- Resource-centric monitoring
- Label-based filtering
- Server-side filtering validation
- JSON export functionality
- Event lifecycle (ADDED/UPDATED/DELETED)

**Usage**:
```bash
cd tests/e2e
go test -v                    # Run all E2E tests
go test -v -run TestFaroTest1 # Run specific test
```

**Structure**:
- `faro_test.go` - Main E2E test suite
- `configs/` - Test configuration files
- `manifests/` - Kubernetes resource manifests
- `logs/` - Test output and JSON exports
- `go.mod` - E2E test dependencies

### Integration Tests (`integration/`)

**Purpose**: Test Faro library functionality directly without using the binary

**Coverage**:
- Vanilla library functionality (migrated from test8)
- Dynamic namespace discovery (migrated from test10)
- Event handler patterns
- Controller lifecycle management
- Direct library API usage

**Usage**:
```bash
cd tests/integration
go test -v                           # Run all integration tests
go test -v -run TestVanillaLibrary   # Run specific test
```

**Structure**:
- `vanilla_library_test.go` - Direct Faro library usage test
- `dynamic_discovery_test.go` - Dynamic controller creation test
- `go.mod` - Integration test dependencies
- `logs/` - Test output and logs

### Archived Tests (`archive/`)

**Purpose**: Legacy tests kept for reference

**Contents**:
- Shell-based E2E tests (deprecated)
- Obsolete Go test files
- Audit scripts

**Note**: These are **not maintained** and should not be used for active development.

## 🚀 Running Tests

### Prerequisites
- Go 1.21+
- Access to a Kubernetes cluster (for E2E and integration tests)
- `kubectl` configured and working

### Quick Start

```bash
# Run all tests
make test

# Run individual test suites
make test-unit        # Unit tests (no K8s cluster required)
make test-e2e         # E2E tests (requires K8s cluster)
make test-integration # Integration tests (requires K8s cluster)

# Run specific tests
cd tests/e2e && go test -v -run TestFaroTest1
cd tests/integration && go test -v -run TestVanillaLibrary
```

### Test Features

**Unit Tests**:
- Configuration validation and normalization
- Utility function testing
- No external dependencies

**E2E Tests**:
- Uses Faro binary (black-box testing)
- Server-side filtering validation
- JSON export verification
- Readiness callback testing

**Integration Tests**:
- Uses Faro library directly
- Readiness callback mechanism
- Dynamic controller creation
- JSON event validation

## 📋 Test Scenarios

### Unit Test Coverage
- ✅ Configuration validation
- ✅ Config normalization
- ✅ Log level handling
- ✅ Path utilities

### E2E Test Coverage
- ✅ **Test 1**: Namespace-centric ConfigMap monitoring
- ✅ **Test 2**: Resource-centric ConfigMap monitoring
- ✅ **Test 3**: Label-based filtering
- ✅ **Test 4**: Resource label-based filtering
- ✅ **Test 5**: Namespace-only monitoring
- ✅ **Test 6**: Combined namespace + resource monitoring
- ✅ **Test 7**: Dual ConfigMap monitoring with wildcards
- ✅ **Test 8**: Multiple namespaces with label selectors

## 🔧 Development Guidelines

### Adding Unit Tests
1. Create test files in `tests/unit/`
2. Follow naming convention: `*_test.go`
3. Test public functions from `pkg/`
4. Use table-driven tests for multiple scenarios

### Adding E2E Tests
1. Add test functions to `tests/e2e/faro_test.go`
2. Create corresponding configs in `tests/e2e/configs/`
3. Create Kubernetes manifests in `tests/e2e/manifests/`
4. Follow the pattern: config → expected data → K8s actions → validation

### Test Naming
- **Unit tests**: `TestFunctionName`
- **E2E tests**: `TestFaroTestN<Description>`
- **Config files**: `simple-test-N.yaml`
- **Manifests**: `testN-manifest.yaml` and `testN-manifest-update.yaml`

## 🐛 Debugging

### Unit Test Failures
```bash
cd tests/unit
go test -v -run TestSpecificFunction
```

### E2E Test Failures
```bash
cd tests/e2e
go test -v -run TestFaroTest1

# Check logs
ls -la logs/test1/logs/
cat logs/test1/logs/faro-*.log
cat logs/test1/logs/events-*.json
```

### Common Issues
- **E2E tests fail**: Check Kubernetes cluster connectivity
- **Path errors**: Ensure working directory is correct
- **Import errors**: Run `go mod tidy` in test directories

## 📈 Test Metrics

### Test Coverage
- **Unit Tests**: 4 test functions, 11 sub-tests
- **E2E Tests**: 8 comprehensive scenarios
- **Integration Tests**: 2 specialized tests
- **Success Rate**: 100%

### Key Improvements
- **Event-driven waiting** via readiness callbacks instead of arbitrary sleep delays
- **Consolidated** duplicate code into shared utilities
- **Server-side filtering** validation ensures no client-side filtering

## 🎯 Future Improvements

### Planned Additions
- [ ] Controller unit tests
- [ ] Performance benchmarks
- [ ] Chaos engineering tests
- [ ] Multi-cluster E2E tests
- [ ] Memory leak detection tests

### Test Infrastructure
- [ ] Parallel E2E test execution
- [ ] Test result reporting
- [ ] Automated test environment setup
- [ ] Integration with CI/CD pipelines