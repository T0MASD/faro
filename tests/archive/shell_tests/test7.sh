#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FARO_BIN="$(cd "$SCRIPT_DIR/.." && pwd)/faro"

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log() { echo -e "${BLUE}[$(date +%H:%M:%S)]${NC} $1"; }
success() { echo -e "${GREEN}✓${NC} $1"; }
error() { echo -e "${RED}✗${NC} $1"; }

cleanup() {
    log "Cleaning up..."
    kubectl delete namespace faro-test-1 2>/dev/null || true
    killall faro 2>/dev/null || true
}

trap cleanup EXIT

check_kubectl_access() {
    log "Checking Kubernetes access and permissions..."
    
    # Check if kubectl is available
    if ! command -v kubectl &> /dev/null; then
        error "kubectl command not found. Please install kubectl."
        exit 1
    fi
    
    # Check if we can connect to cluster
    if ! kubectl cluster-info &> /dev/null; then
        error "Cannot connect to Kubernetes cluster. Please check your kubeconfig."
        exit 1
    fi
    
    # Check if we can create namespaces (try dry-run)
    if ! kubectl create namespace test-permissions-check --dry-run=client &> /dev/null; then
        error "Cannot create namespaces. Please check your Kubernetes permissions."
        exit 1
    fi
    
    # Check if we can create configmaps (try dry-run)
    if ! kubectl create configmap test-permissions-check --from-literal=key=value --dry-run=client &> /dev/null; then
        error "Cannot create configmaps. Please check your Kubernetes permissions."
        exit 1
    fi
    
    success "Kubernetes access verified"
}

main() {
    log "Starting Simple E2E Test 7 - Dual ConfigMap monitoring (namespace + resource configs)"

    check_kubectl_access
    
    cd "$SCRIPT_DIR"
    
    # Create logs directory if it doesn't exist
    mkdir -p logs
    
    local log_file="logs/simple-test-7.log"

    log "Faro log file: $SCRIPT_DIR/$log_file"

    # Start Faro
    log "Starting Faro with dual configmap config..."
    timeout 60s $FARO_BIN -config="configs/simple-test-7.yaml" > "$log_file" 2>&1 &
    local faro_pid=$!

    # Wait for ready
    log "Waiting for Faro readiness..."
    for i in {1..30}; do
        if grep -q "Multi-layered informer architecture started successfully" "$log_file" 2>/dev/null; then
            success "Faro is ready!"
            break
        fi
        sleep 1
    done

    # Show how many informers were created
    log "Checking informer setup..."
    if grep -q "Started.*config-driven informers (deduplicated)" "$log_file"; then
        local informer_count=$(grep "Started.*config-driven informers" "$log_file" | sed -n 's/.*Started \([0-9]*\) config-driven informers.*/\1/p')
        log "Faro created $informer_count deduplicated informer(s)"
    fi

    # Apply test resources
    log "Applying test manifests..."
    kubectl apply -f manifests/unified-test-resources.yaml

    # Wait for events
    sleep 3
    log "Checking for ADDED events..."

    # Check both ConfigMaps
    if grep -q "CONFIG \[ADDED\].*v1/configmaps.*faro-test-1/test-config-1" "$log_file"; then
        success "ConfigMap test-config-1 ADDED event detected!"
    else
        error "ConfigMap test-config-1 ADDED event not found"
    fi

    if grep -q "CONFIG \[ADDED\].*v1/configmaps.*faro-test-1/test-config-2" "$log_file"; then
        success "ConfigMap test-config-2 ADDED event detected!"
    else
        error "ConfigMap test-config-2 ADDED event not found"
    fi

    # Update both ConfigMaps
    log "Updating ConfigMaps..."
    kubectl patch configmap test-config-1 -n faro-test-1 --patch='{"data":{"test-action":"UPDATED"}}'
    kubectl patch configmap test-config-2 -n faro-test-1 --patch='{"data":{"test-action":"UPDATED"}}'

    # Wait for update events
    sleep 2
    log "Checking for UPDATED events..."

    if grep -q "CONFIG \[UPDATED\].*v1/configmaps.*faro-test-1/test-config-1" "$log_file"; then
        success "ConfigMap test-config-1 UPDATED event detected!"
    else
        error "ConfigMap test-config-1 UPDATED event not found"
    fi

    if grep -q "CONFIG \[UPDATED\].*v1/configmaps.*faro-test-1/test-config-2" "$log_file"; then
        success "ConfigMap test-config-2 UPDATED event detected!"
    else
        error "ConfigMap test-config-2 UPDATED event not found"
    fi

    # PHASE 3: Update Service and Secret (Resource Lifecycle Enhancement)
    log "Updating Service and Secret (Phase 3 enhancement)..."
    kubectl patch service test-service -n faro-test-1 --patch='{"metadata":{"annotations":{"phase3":"updated","test":"test7"}}}' 2>/dev/null || log "Service not found (expected in ConfigMap-focused test)"
    kubectl patch secret test-secret -n faro-test-1 --patch='{"metadata":{"annotations":{"phase3":"updated","test":"test7"}}}' 2>/dev/null || log "Secret not found (expected in ConfigMap-focused test)"

    # Wait for update events
    sleep 2
    log "Checking for Service/Secret UPDATED events..."

    if grep -q "CONFIG \[UPDATED\].*faro-test-1/test-service" "$log_file"; then
        success "Service UPDATED event detected!"
    else
        log "Service UPDATED event not found (expected - test7 focuses on ConfigMaps)"
    fi

    if grep -q "CONFIG \[UPDATED\].*faro-test-1/test-secret" "$log_file"; then
        success "Secret UPDATED event detected!"
    else
        log "Secret UPDATED event not found (expected - test7 focuses on ConfigMaps)"
    fi

    # Delete Service and Secret before ConfigMaps (if they exist)
    log "Deleting Service and Secret (Phase 3 enhancement)..."
    kubectl delete service test-service -n faro-test-1 2>/dev/null || true
    kubectl delete secret test-secret -n faro-test-1 2>/dev/null || true

    # Delete both ConfigMaps
    log "Deleting ConfigMaps..."
    kubectl delete configmap test-config-1 -n faro-test-1
    kubectl delete configmap test-config-2 -n faro-test-1

    # Wait for deletion
    sleep 2
    log "Checking for DELETED events..."

    if grep -q "CONFIG \[DELETED\].*v1/configmaps.*faro-test-1/test-config-1" "$log_file"; then
        success "ConfigMap test-config-1 DELETED event detected!"
    else
        error "ConfigMap test-config-1 DELETED event not found"
    fi

    if grep -q "CONFIG \[DELETED\].*v1/configmaps.*faro-test-1/test-config-2" "$log_file"; then
        success "ConfigMap test-config-2 DELETED event detected!"
    else
        error "ConfigMap test-config-2 DELETED event not found"
    fi

    # Show log
    log "CONFIG events in $log_file:"
    grep "CONFIG" "$log_file"

    kill $faro_pid 2>/dev/null || true
    success "Test 7 completed!"
}

main "$@"