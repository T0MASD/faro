#!/bin/bash

# Simple E2E Test 8 - Vanilla Faro functionality via library
# This test replicates exact vanilla Faro behavior using library (no custom CLI features)
# Same as test1 but using library instead of binary

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Enhanced logging function
log() {
    echo "[$(date '+%H:%M:%S')] $1"
}

# Function to check kubectl access
check_kubectl_access() {
    log "Checking Kubernetes access and permissions..."
    
    if ! kubectl auth can-i create namespaces >/dev/null 2>&1; then
        echo "❌ Error: Cannot create namespaces. Please check your Kubernetes permissions."
        echo "   Required permissions: create/delete namespaces, create/update/delete configmaps"
        exit 1
    fi
    
    if ! kubectl auth can-i create configmaps >/dev/null 2>&1; then
        echo "❌ Error: Cannot create configmaps. Please check your Kubernetes permissions."
        exit 1
    fi
    
    echo "✅ Kubernetes access verified"
}

# Function to cleanup resources
cleanup() {
    if [ $? -ne 0 ]; then
        log "Test failed, cleaning up..."
    else
        log "Cleaning up..."
    fi
    
    # Kill the faro process if it's still running
    if [ ! -z "$FARO_PID" ]; then
        kill $FARO_PID 2>/dev/null || true
        wait $FARO_PID 2>/dev/null || true
    fi
    
    # Clean up Kubernetes resources
    kubectl delete namespace faro-test-1 --ignore-not-found=true >/dev/null 2>&1 || true
}

# Set up cleanup trap
trap cleanup EXIT

log "Starting Vanilla Library Test 8 - Namespace-Centric Config (like test1)"

# Check Kubernetes access
check_kubectl_access

# Ensure logs directory exists
mkdir -p logs

log_file="logs/simple-test-8.log"
log "Faro log file: $SCRIPT_DIR/$log_file"

log "Starting Faro via library (go run test8.go)..."

# Start faro via library in background - vanilla functionality only
go run test8.go > $log_file 2>&1 &
FARO_PID=$!

log "Waiting for Faro readiness..."

# Wait for Faro to be ready
timeout=30
for i in $(seq 1 $timeout); do
    if grep -q "Multi-layered informer architecture started successfully" $log_file 2>/dev/null; then
        echo "✅ Faro is ready!"
        break
    fi
    
    if [ $i -eq $timeout ]; then
        echo "❌ Timeout waiting for Faro readiness"
        echo "--- Faro output ---"
        cat $log_file 2>/dev/null || echo "No output file found"
        exit 1
    fi
    
    sleep 1
done

log "Applying test manifests..."

# Apply manifests (create namespace and configmaps)
kubectl apply -f manifests/unified-test-resources.yaml

# Wait for events to be processed
sleep 5

log "Checking for ADDED events in: $log_file"
if grep -q "CONFIG \[ADDED\].*test-config-1" $log_file; then
    echo "✅ ConfigMap ADDED event detected!"
else
    echo "❌ ConfigMap ADDED event not found"
    exit 1
fi

# Verify test-config-2 is processed (no client-side filtering in Faro core)
if grep -q "CONFIG \[ADDED\].*test-config-2" $log_file; then
    echo "✅ ConfigMap test-config-2 ADDED event processed (no client-side filtering)"
else
    echo "❌ ConfigMap test-config-2 ADDED event should be processed (no client-side filtering in Faro core)!"
    exit 1
fi

log "Updating ConfigMaps..."
kubectl patch configmap test-config-1 -n faro-test-1 --patch '{"data":{"updated":"true"}}'
kubectl patch configmap test-config-2 -n faro-test-1 --patch '{"data":{"updated":"true"}}'

# Wait for update event
sleep 3

log "Checking for UPDATED events..."
if grep -q "CONFIG \[UPDATED\].*test-config-1" $log_file; then
    echo "✅ ConfigMap UPDATED event detected!"
else
    echo "❌ ConfigMap UPDATED event not found"
    exit 1
fi

# Verify test-config-2 UPDATE is processed (no client-side filtering in Faro core)
if grep -q "CONFIG \[UPDATED\].*test-config-2" $log_file; then
    echo "✅ ConfigMap test-config-2 UPDATED event processed (no client-side filtering)"
else
    echo "❌ ConfigMap test-config-2 UPDATED event should be processed (no client-side filtering in Faro core)!"
    exit 1
fi

log "Deleting ConfigMaps..."
kubectl delete configmap test-config-1 -n faro-test-1
kubectl delete configmap test-config-2 -n faro-test-1

# Wait for delete event
sleep 4

log "Checking for DELETED events..."
if grep -q "CONFIG \[DELETED\].*test-config-1" $log_file; then
    echo "✅ ConfigMap DELETED event detected!"
else
    echo "❌ ConfigMap DELETED event not found"
    exit 1
fi

# Verify test-config-2 DELETE is processed (no client-side filtering in Faro core)
if grep -q "CONFIG \[DELETED\].*test-config-2" $log_file; then
    echo "✅ ConfigMap test-config-2 DELETED event processed (no client-side filtering)"
else
    echo "❌ ConfigMap test-config-2 DELETED event should be processed (no client-side filtering in Faro core)!"
    exit 1
fi

# Show log
log "CONFIG events in $log_file:"
grep "CONFIG" "$log_file"

log "✅ Test 8 completed!"
log "📋 Summary:"
log "   - Used library to replicate vanilla Faro functionality"
log "   - Same behavior as test1 but via go run test8.go"
log "   - All events detected: ADDED, UPDATED, DELETED"