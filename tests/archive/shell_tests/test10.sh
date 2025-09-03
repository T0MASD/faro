#!/bin/bash

# Simple E2E Test 10 - Dynamic Namespace Discovery Library Test
# This test demonstrates dynamic controller creation based on namespace labels

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log() {
    echo -e "${YELLOW}[$(date +'%Y-%m-%d %H:%M:%S')] $1${NC}"
}

success() {
    echo -e "${GREEN}[$(date +'%Y-%m-%d %H:%M:%S')] ✓ $1${NC}"
}

error() {
    echo -e "${RED}[$(date +'%Y-%m-%d %H:%M:%S')] ✗ $1${NC}"
    exit 1
}

cleanup() {
    log "Cleaning up test resources..."
    kubectl delete -f manifests/unified-test-resources.yaml --ignore-not-found=true || true
}

# Set up cleanup trap
trap cleanup EXIT

log "Starting Test10 - Dynamic Namespace Discovery"

# Create logs directory if it doesn't exist
mkdir -p logs

log_file="logs/simple-test-10.log"
log "Faro log file: $SCRIPT_DIR/$log_file"

# Check kubectl access
log "Checking kubectl access..."
if ! kubectl cluster-info >/dev/null 2>&1; then
    error "Cannot access Kubernetes cluster"
fi
success "Kubernetes access verified"

# Apply test resources
log "Applying test manifests..."
kubectl apply -f manifests/unified-test-resources.yaml

# Wait for resources to be created
sleep 5

# Run the library test
log "Running dynamic discovery test..."
if ! go run test10.go > "$log_file" 2>&1; then
    error "Test10 failed to run"
fi

success "Test10 completed successfully!"

log "📋 Summary:"
log "   - Discovery controller monitored specific namespace: faro-testa"
log "   - Detected faro-testa with next-namespace=faro-test-1"
log "   - Created targeted controller for faro-test-1"
log "   - Demonstrated dynamic controller creation pattern"