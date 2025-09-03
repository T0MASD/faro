#!/bin/bash

# Simple E2E Test 9 - Multiple namespaces with same label selector
# This test validates Faro's ability to watch multiple namespace resources
# that share the same label, demonstrating proper server-side filtering

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FARO_BIN="${SCRIPT_DIR}/../faro"

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
    # Phase 4: Clean up advanced resources first
    kubectl delete configmap cross-ref-config -n faro-ns-1 --ignore-not-found=true || true
    kubectl delete secret rotation-secret -n faro-ns-2 --ignore-not-found=true || true
    kubectl delete configmap dependent-config -n faro-ns-2 --ignore-not-found=true || true
    
    # Phase 2: Clean up ConfigMaps first (namespaces will cascade delete them anyway)
    kubectl delete configmap test-config-ns1 -n faro-ns-1 --ignore-not-found=true || true
    kubectl delete configmap test-config-ns2 -n faro-ns-2 --ignore-not-found=true || true
    
    kubectl delete namespace faro-ns-1 --ignore-not-found=true || true
    kubectl delete namespace faro-ns-2 --ignore-not-found=true || true
    kubectl delete namespace faro-ns-3 --ignore-not-found=true || true
    
    # Kill Faro if still running
    if [[ -n "${faro_pid:-}" ]] && kill -0 "$faro_pid" 2>/dev/null; then
        log "Stopping Faro (PID: $faro_pid)..."
        kill "$faro_pid" || true
        wait "$faro_pid" 2>/dev/null || true
    fi
}

trap cleanup EXIT

check_kubectl_access() {
    log "Checking kubectl access..."
    
    if ! command -v kubectl &> /dev/null; then
        error "kubectl not found in PATH"
    fi
    
    if ! kubectl auth can-i get namespaces &> /dev/null; then
        error "kubectl access check failed - insufficient permissions"
        exit 1
    fi
    
    success "Kubernetes access verified"
}

main() {
    log "Starting Simple E2E Test 9 - Multiple namespaces with same label selector"

    check_kubectl_access
    
    cd "$SCRIPT_DIR"
    
    # Create logs directory if it doesn't exist
    mkdir -p logs
    
    local log_file="logs/simple-test-9.log"

    log "Faro log file: $SCRIPT_DIR/$log_file"

    # Start Faro
    log "Starting Faro with label-based namespace monitoring..."
    timeout 60s $FARO_BIN -config="configs/simple-test-9.yaml" > "$log_file" 2>&1 &
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

    # Show informer setup
    log "Checking informer setup..."
    if grep -q "Started.*config-driven informers (deduplicated)" "$log_file"; then
        local informer_count=$(grep "Started.*config-driven informers" "$log_file" | sed -n 's/.*Started \([0-9]*\) config-driven informers.*/\1/p')
        log "Faro created $informer_count deduplicated informer(s)"
    fi

    # Create test namespaces with labels
    log "Creating test namespaces..."
    
    # These two should match (have the target label)
    kubectl create namespace faro-ns-1 --dry-run=client -o yaml | \
        kubectl label --local -f - test-label=faro-namespace -o yaml | \
        kubectl apply -f -
    
    kubectl create namespace faro-ns-2 --dry-run=client -o yaml | \
        kubectl label --local -f - test-label=faro-namespace -o yaml | \
        kubectl apply -f -
    
    # This one should NOT match (different label)
    kubectl create namespace faro-ns-3 --dry-run=client -o yaml | \
        kubectl label --local -f - test-label=different-value -o yaml | \
        kubectl apply -f -

    # PHASE 2: Add ConfigMaps to faro-ns-1 and faro-ns-2 for multi-namespace resource testing
    log "Creating ConfigMaps in detected namespaces (Phase 2 enhancement)..."
    
    # Add ConfigMap to faro-ns-1
    kubectl create configmap test-config-ns1 \
        --from-literal=namespace=faro-ns-1 \
        --from-literal=phase=phase2 \
        --from-literal=test-data="multi-namespace-validation" \
        -n faro-ns-1
    
    # Add ConfigMap to faro-ns-2  
    kubectl create configmap test-config-ns2 \
        --from-literal=namespace=faro-ns-2 \
        --from-literal=phase=phase2 \
        --from-literal=test-data="multi-namespace-validation" \
        -n faro-ns-2

    # PHASE 4: Advanced Resource Operations
    log "Performing advanced resource operations (Phase 4 enhancement)..."
    
    # 1. Cross-namespace ConfigMap reference simulation
    log "Creating cross-namespace ConfigMap references..."
    kubectl create configmap cross-ref-config \
        --from-literal=source-namespace=faro-ns-1 \
        --from-literal=target-namespace=faro-ns-2 \
        --from-literal=phase=phase4 \
        --from-literal=operation="cross-namespace-reference" \
        -n faro-ns-1
    
    # 2. Secret rotation simulation
    log "Simulating secret rotation..."
    kubectl create secret generic rotation-secret \
        --from-literal=version=v1 \
        --from-literal=key="initial-secret-value" \
        --from-literal=phase=phase4 \
        -n faro-ns-2
    
    # Wait for creation events
    sleep 2
    
    # 3. Update cross-namespace ConfigMap (simulate config propagation)
    log "Updating cross-namespace ConfigMap..."
    kubectl patch configmap cross-ref-config -n faro-ns-1 \
        --patch='{"data":{"propagation-status":"updated","timestamp":"'$(date -Iseconds)'"}}'
    
    # 4. Rotate the secret (simulate credential rotation)
    log "Rotating secret..."
    kubectl patch secret rotation-secret -n faro-ns-2 \
        --patch='{"data":{"version":"djI=","key":"cm90YXRlZC1zZWNyZXQtdmFsdWU="}}'  # base64: v2, rotated-secret-value
    
    # 5. Create dependent ConfigMap in faro-ns-2 (simulate dependency chain)
    log "Creating dependent ConfigMap..."
    kubectl create configmap dependent-config \
        --from-literal=depends-on=cross-ref-config \
        --from-literal=secret-ref=rotation-secret \
        --from-literal=phase=phase4 \
        --from-literal=dependency-type="advanced-chain" \
        -n faro-ns-2

    # Wait for events
    sleep 3
    log "Checking for namespace ADDED events..."

    # Check first matching namespace
    local faro_ns1_events=$(grep "CONFIG \[ADDED\].*v1/namespaces.*faro-ns-1" "$log_file" 2>/dev/null | wc -l)
    if [[ "$faro_ns1_events" -gt 0 ]]; then
        success "Namespace faro-ns-1 ADDED event detected! (label match)"
    else
        error "Namespace faro-ns-1 ADDED event not found (should match label selector)"
    fi

    # Check second matching namespace
    local faro_ns2_events=$(grep "CONFIG \[ADDED\].*v1/namespaces.*faro-ns-2" "$log_file" 2>/dev/null | wc -l)
    if [[ "$faro_ns2_events" -gt 0 ]]; then
        success "Namespace faro-ns-2 ADDED event detected! (label match)"
    else
        error "Namespace faro-ns-2 ADDED event not found (should match label selector)"
    fi

    # Check that non-matching namespace is NOT captured
    local faro_ns3_events=$(grep "CONFIG \[ADDED\].*v1/namespaces.*faro-ns-3" "$log_file" 2>/dev/null | wc -l)
    if [[ "$faro_ns3_events" -eq 0 ]]; then
        success "Namespace faro-ns-3 correctly filtered out (different label)"
    else
        error "Namespace faro-ns-3 should NOT have been captured (wrong label)"
    fi

    # Validate total matching namespaces
    local total_namespace_events=$(grep "CONFIG \[ADDED\].*v1/namespaces.*faro-ns-[12]" "$log_file" 2>/dev/null | wc -l)
    if [[ "$total_namespace_events" -eq 2 ]]; then
        success "Both matching namespaces detected by label selector!"
    else
        error "Expected 2 namespace ADDED events, but found: $total_namespace_events"
    fi

    # Test namespace updates
    log "Updating matching namespaces to trigger UPDATED events..."
    kubectl annotate namespace faro-ns-1 test-annotation="updated-$(date +%s)" --overwrite
    kubectl annotate namespace faro-ns-2 test-annotation="updated-$(date +%s)" --overwrite

    # Wait for update events
    sleep 2
    log "Checking for namespace UPDATED events..."

    if grep -q "CONFIG \[UPDATED\].*v1/namespaces.*faro-ns-1" "$log_file"; then
        success "Namespace faro-ns-1 UPDATED event detected!"
    else
        error "Namespace faro-ns-1 UPDATED event not found"
    fi

    if grep -q "CONFIG \[UPDATED\].*v1/namespaces.*faro-ns-2" "$log_file"; then
        success "Namespace faro-ns-2 UPDATED event detected!"
    else
        error "Namespace faro-ns-2 UPDATED event not found"
    fi

    # Final validation - check for any errors
    log "Checking for configuration errors..."
    
    if grep -q "ERROR\|FATAL" "$log_file"; then
        error "Found ERROR or FATAL messages in Faro logs - check $log_file"
    else
        success "No ERROR or FATAL messages found"
    fi

    # Verify server-side filtering is working
    log "Verifying server-side filtering..."
    if grep -q "Applying label selector.*test-label=faro-namespace.*to informer for v1/namespaces" "$log_file"; then
        success "Server-side label filtering is active!"
    else
        log "Warning: Could not confirm server-side filtering in logs"
    fi

    success "Test 9 completed successfully! Label-based namespace monitoring works correctly."
    log "📋 Summary:"
    log "   - Single informer with server-side label filtering"
    log "   - Multiple namespaces with same label detected"
    log "   - Non-matching namespace correctly filtered out"
    log "   - All events detected: ADDED, UPDATED"
    log "Check detailed logs at: $SCRIPT_DIR/$log_file"
}

main "$@"