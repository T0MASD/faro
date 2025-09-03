#!/bin/bash
set -e

# Simple E2E Test 5 - Namespace + Two ConfigMaps (by name and label)
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
    log "Starting Simple E2E Test 5 - Namespace monitoring only"

    check_kubectl_access
    
    cd "$SCRIPT_DIR"
    
    # Create logs directory if it doesn't exist
    mkdir -p logs
    
    local log_file="logs/simple-test-5.log"

    log "Faro log file: $SCRIPT_DIR/$log_file"

    # Start Faro
    log "Starting Faro with namespace monitoring config..."
    timeout 60s $FARO_BIN -config="configs/simple-test-5.yaml" > "$log_file" 2>&1 &
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

    # Apply test resources (creates namespace)
    log "Applying test manifests..."
    kubectl apply -f manifests/unified-test-resources.yaml

    # PHASE 4: Advanced Service Discovery Operations
    log "Performing advanced service discovery (Phase 4 enhancement)..."
    
    # 1. Create a service with multiple endpoints
    kubectl create service clusterip discovery-service \
        --tcp=80:8080 \
        --tcp=443:8443 \
        -n faro-test-1
    
    # 2. Create a headless service for service discovery
    kubectl create service clusterip headless-service \
        --tcp=80:8080 \
        --clusterip=None \
        -n faro-test-1
    
    # 3. Add service annotations (simulate service mesh integration)
    kubectl annotate service discovery-service \
        phase4.test/service-type="discovery" \
        phase4.test/mesh-enabled="true" \
        -n faro-test-1
    
    # Wait for service creation events
    sleep 2
    
    # 4. Update service (simulate configuration change)
    kubectl patch service discovery-service -n faro-test-1 \
        --patch='{"metadata":{"annotations":{"phase4.test/updated":"true","phase4.test/timestamp":"'$(date -Iseconds)'"}}}'

    # Wait for events
    sleep 3
    log "Checking for ADDED events..."

    # Check namespace creation
    if grep -q "CONFIG \[ADDED\].*v1/namespaces.*faro-test-1" "$log_file"; then
        success "Namespace ADDED event detected!"
    else
        error "Namespace ADDED event not found"
    fi

    # Delete namespace
    log "Deleting namespace..."
    kubectl delete namespace faro-test-1

    # Wait for namespace deletion
    sleep 3
    log "Checking for namespace DELETED event..."

    if grep -q "CONFIG \[DELETED\].*v1/namespaces.*faro-test-1" "$log_file"; then
        success "Namespace DELETED event detected!"
    else
        error "Namespace DELETED event not found"
    fi

    # Show log
    log "CONFIG events in $log_file:"
    grep "CONFIG" "$log_file"

    kill $faro_pid 2>/dev/null || true
    success "Test 5 completed!"
}

main "$@"