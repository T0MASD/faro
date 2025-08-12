#!/bin/bash
set -e

# Simple E2E Test 4 - Resource ConfigMap by Label
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FARO_BIN="$(cd "$SCRIPT_DIR/.." && pwd)/faro"

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m'

log() { echo -e "${BLUE}[$(date '+%H:%M:%S')] $1${NC}"; }
success() { echo -e "${GREEN}✓ $1${NC}"; }
error() { echo -e "${RED}✗ $1${NC}"; }

cleanup() {
    log "Cleaning up..."
    kubectl delete namespace faro-test-1 --ignore-not-found=true 2>/dev/null || true
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
    log "Starting Simple E2E Test 4 - Resource Label-based Config"

    check_kubectl_access
    
    cd "$SCRIPT_DIR"
    
    # Create logs directory if it doesn't exist
    mkdir -p logs
    
    local log_file="logs/simple-test-4.log"

    log "Faro log file: $SCRIPT_DIR/$log_file"

    # Start Faro
    log "Starting Faro with resource label-based config..."
    timeout 60s $FARO_BIN -config="configs/simple-test-4.yaml" > "$log_file" 2>&1 &
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

    # Apply test resources
    log "Applying test manifests..."
    kubectl apply -f manifests/unified-test-resources.yaml

    # Wait for events
    sleep 3
    log "Checking for ADDED events in: $log_file"

    if grep -q "CONFIG \[ADDED\].*faro-test-1/test-config-1" "$log_file"; then
        success "ConfigMap ADDED event detected!"
    else
        error "ConfigMap ADDED event not found"
    fi

    # Verify test-config-2 is filtered out (negative test case)
    if grep -q "CONFIG \[ADDED\].*faro-test-1/test-config-2" "$log_file"; then
        error "ConfigMap test-config-2 ADDED event should have been filtered out (name doesn't match pattern)!"
    else
        success "ConfigMap test-config-2 correctly filtered out (name doesn't match pattern)"
    fi

    # Update ConfigMap
    log "Updating ConfigMap..."
    kubectl patch configmap test-config-1 -n faro-test-1 --patch='{"data":{"test-action":"UPDATED"}}'

    # Wait for update event
    sleep 2
    log "Checking for UPDATED events..."

    if grep -q "CONFIG \[UPDATED\].*faro-test-1/test-config-1" "$log_file"; then
        success "ConfigMap UPDATED event detected!"
    else
        error "ConfigMap UPDATED event not found"
    fi

    # Verify test-config-2 UPDATE is filtered out (negative test case)
    if grep -q "CONFIG \[UPDATED\].*faro-test-1/test-config-2" "$log_file"; then
        error "ConfigMap test-config-2 UPDATED event should have been filtered out (name doesn't match pattern)!"
    else
        success "ConfigMap test-config-2 UPDATED event correctly filtered out (name doesn't match pattern)"
    fi

    # Delete ConfigMap
    log "Deleting ConfigMap test-config..."
    kubectl delete configmap test-config-1 -n faro-test-1

    # Wait for deletion event
    sleep 3
    log "Checking for DELETED events..."

    if grep -q "CONFIG \[DELETED\].*faro-test-1/test-config-1" "$log_file" 2>/dev/null; then
        success "ConfigMap DELETED event detected!"
    else
        error "ConfigMap DELETED event not found"
    fi

    # Verify test-config-2 DELETE is filtered out (negative test case)
    if grep -q "CONFIG \[DELETED\].*faro-test-1/test-config-2" "$log_file"; then
        error "ConfigMap test-config-2 DELETED event should have been filtered out (name doesn't match pattern)!"
    else
        success "ConfigMap test-config-2 DELETED event correctly filtered out (name doesn't match pattern)"
    fi

    # Show log
    log "CONFIG events in $log_file:"
    grep "CONFIG" "$log_file"

    kill $faro_pid 2>/dev/null || true
    success "Test 4 completed!"
}

main "$@"