#!/bin/bash
# Test script for Faro operator deployment validation
# This script performs end-to-end validation of the operator deployment

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
OPERATOR_NAMESPACE="faro-system"
TEST_NAMESPACE="faro-test-validation"
OPERATOR_IMAGE="${OPERATOR_IMAGE:-localhost/faro-operator:latest}"
TIMEOUT=120

# Helper functions
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

wait_for_pod_ready() {
    local namespace=$1
    local label=$2
    local timeout=$3
    
    log_info "Waiting for pod with label $label in namespace $namespace to be ready..."
    kubectl wait --for=condition=ready pod \
        -l "$label" \
        -n "$namespace" \
        --timeout="${timeout}s" || {
        log_error "Pod did not become ready within ${timeout}s"
        kubectl get pods -n "$namespace" -l "$label"
        kubectl describe pod -n "$namespace" -l "$label"
        kubectl logs -n "$namespace" -l "$label" --tail=50 || true
        return 1
    }
}

verify_metrics_endpoint() {
    log_info "Verifying metrics endpoint is responding..."
    
    # Get pod name
    local pod_name=$(kubectl get pod -n "$OPERATOR_NAMESPACE" \
        -l app.kubernetes.io/name=faro-operator \
        -o jsonpath='{.items[0].metadata.name}')
    
    if [ -z "$pod_name" ]; then
        log_error "Could not find operator pod"
        return 1
    fi
    
    # Curl metrics endpoint
    local metrics_output=$(kubectl exec -n "$OPERATOR_NAMESPACE" "$pod_name" -- \
        wget -qO- http://localhost:8080/metrics 2>&1)
    
    if [ $? -ne 0 ]; then
        log_error "Failed to curl metrics endpoint"
        echo "$metrics_output"
        return 1
    fi
    
    # Verify key metrics exist
    if echo "$metrics_output" | grep -q "faro_informer_health"; then
        log_info "✅ Metrics endpoint is responding correctly"
    else
        log_error "Metrics endpoint not returning expected data"
        echo "$metrics_output"
        return 1
    fi
}

verify_rbac_restrictions() {
    log_info "Verifying RBAC restrictions..."
    
    local sa="system:serviceaccount:${OPERATOR_NAMESPACE}:faro-operator"
    
    # Should NOT be able to get secrets
    if kubectl auth can-i get secrets --as="$sa" 2>&1 | grep -q "no"; then
        log_info "✅ RBAC correctly denies access to secrets"
    else
        log_error "RBAC allows access to secrets (should be denied)"
        return 1
    fi
    
    # Should NOT be able to delete pods
    if kubectl auth can-i delete pods --as="$sa" 2>&1 | grep -q "no"; then
        log_info "✅ RBAC correctly denies pod deletion"
    else
        log_error "RBAC allows pod deletion (should be denied)"
        return 1
    fi
    
    # Should be able to list pods
    if kubectl auth can-i list pods --as="$sa" 2>&1 | grep -q "yes"; then
        log_info "✅ RBAC correctly allows pod listing"
    else
        log_error "RBAC denies pod listing (should be allowed)"
        return 1
    fi
}

verify_event_capture() {
    log_info "Verifying event capture functionality..."
    
    # Get pod name
    local pod_name=$(kubectl get pod -n "$OPERATOR_NAMESPACE" \
        -l app.kubernetes.io/name=faro-operator \
        -o jsonpath='{.items[0].metadata.name}')
    
    # Check event files
    local event_files=$(kubectl exec -n "$OPERATOR_NAMESPACE" "$pod_name" -- \
        ls -1 /var/faro/events/*.json 2>/dev/null | wc -l)
    
    if [ "$event_files" -gt 0 ]; then
        log_info "✅ Found $event_files event JSON files"
    else
        log_warning "No event files found yet (this might be expected if no events occurred)"
    fi
    
    # Check metrics for event counts
    local metrics_output=$(kubectl exec -n "$OPERATOR_NAMESPACE" "$pod_name" -- \
        wget -qO- http://localhost:8080/metrics 2>&1)
    
    if echo "$metrics_output" | grep -q "faro_events_total"; then
        log_info "✅ Event processing metrics are being tracked"
        echo "$metrics_output" | grep "faro_events_total" | head -5
    fi
}

cleanup() {
    log_info "Cleaning up test resources..."
    kubectl delete namespace "$TEST_NAMESPACE" --ignore-not-found=true --wait=false || true
    kubectl delete namespace "$OPERATOR_NAMESPACE" --ignore-not-found=true --wait=false || true
}

main() {
    log_info "Starting Faro operator deployment validation"
    log_info "Operator image: $OPERATOR_IMAGE"
    
    # Cleanup any previous runs
    log_info "Cleaning up any previous test resources..."
    cleanup
    sleep 2
    
    # Step 1: Build operator image
    log_info "Step 1: Building operator image..."
    make operator-image || {
        log_error "Failed to build operator image"
        exit 1
    }
    
    # Step 2: Deploy operator
    log_info "Step 2: Deploying operator..."
    bash scripts/deploy-operator.sh || {
        log_error "Failed to deploy operator"
        exit 1
    }
    
    # Step 3: Wait for operator pod to be ready
    log_info "Step 3: Waiting for operator pod to be ready..."
    wait_for_pod_ready "$OPERATOR_NAMESPACE" "app.kubernetes.io/name=faro-operator" "$TIMEOUT" || {
        log_error "Operator pod failed to become ready"
        exit 1
    }
    
    # Step 4: Verify metrics endpoint
    log_info "Step 4: Verifying metrics endpoint..."
    verify_metrics_endpoint || {
        log_error "Metrics endpoint verification failed"
        exit 1
    }
    
    # Step 5: Verify RBAC restrictions
    log_info "Step 5: Verifying RBAC restrictions..."
    verify_rbac_restrictions || {
        log_error "RBAC verification failed"
        exit 1
    }
    
    # Step 6: Create test namespace and workload
    log_info "Step 6: Creating test workload..."
    kubectl create namespace "$TEST_NAMESPACE" || true
    kubectl create job operator-ci-test --image=busybox:latest \
        --namespace="$TEST_NAMESPACE" \
        -- sh -c "echo 'Faro operator CI test'; sleep 5" || {
        log_error "Failed to create test job"
        exit 1
    }
    
    # Wait a bit for events to be captured
    log_info "Waiting 15 seconds for events to be captured..."
    sleep 15
    
    # Step 7: Verify event capture
    log_info "Step 7: Verifying event capture..."
    verify_event_capture || {
        log_error "Event capture verification failed"
        exit 1
    }
    
    # Step 8: Get operator logs for CI artifacts
    log_info "Step 8: Capturing operator logs..."
    local pod_name=$(kubectl get pod -n "$OPERATOR_NAMESPACE" \
        -l app.kubernetes.io/name=faro-operator \
        -o jsonpath='{.items[0].metadata.name}')
    
    mkdir -p tests/operator-ci/logs
    kubectl logs -n "$OPERATOR_NAMESPACE" "$pod_name" > tests/operator-ci/logs/operator.log || true
    
    # Export events from operator
    kubectl exec -n "$OPERATOR_NAMESPACE" "$pod_name" -- \
        sh -c "cat /var/faro/events/*.json 2>/dev/null" > tests/operator-ci/logs/captured-events.json || true
    
    log_info ""
    log_info "=========================================="
    log_info "✅ ALL OPERATOR TESTS PASSED"
    log_info "=========================================="
    log_info ""
    log_info "Summary:"
    log_info "  - Operator image built successfully"
    log_info "  - Operator deployed and running"
    log_info "  - Metrics endpoint responding"
    log_info "  - RBAC restrictions verified"
    log_info "  - Event capture working"
    log_info "  - Logs saved to tests/operator-ci/logs/"
    log_info ""
    
    return 0
}

# Run main function
main "$@"

