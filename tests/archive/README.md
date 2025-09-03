# Archived Tests

This directory contains archived test files that are no longer actively maintained but kept for reference.

## Contents

### `shell_tests/`
- **Legacy shell-based E2E tests** (test1.sh through test9.sh)
- **Obsolete Go test files** (test8.go, test10.go, faro_test1_e2e_test.go)
- **Audit scripts** (audit-workload-monitor.sh, universal-audit.sh, etc.)

## Migration

These tests have been **replaced** by the modern Go-based E2E test suite located in `tests/e2e/`.

### Why Archived?
1. **Shell scripts**: Difficult to maintain, debug, and integrate with CI/CD
2. **Obsolete Go tests**: Superseded by comprehensive E2E framework
3. **Inconsistent approach**: Mixed testing methodologies

### Current Testing
- **Unit Tests**: `tests/unit/` - Library code testing
- **E2E Tests**: `tests/e2e/` - End-to-end integration testing

## Usage

These files are kept for **reference only**. For active development, use the tests in the `tests/` directory.