#!/bin/bash

# Comprehensive Audit for Workload Monitor E2E Test
# This script generates expected events from config + manifests, runs the test, and compares actual vs expected

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
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

info() {
    echo -e "${BLUE}[$(date +'%Y-%m-%d %H:%M:%S')] ℹ️  $1${NC}"
}

cleanup() {
    log "Cleanup: Removing test resources..."
    kubectl delete -f manifests/unified-test-resources.yaml --ignore-not-found=true || true
    
    # Kill workload-monitor if it's still running
    if [[ -n "${MONITOR_PID:-}" ]]; then
        log "Cleanup: Stopping workload monitor (PID: $MONITOR_PID)..."
        kill $MONITOR_PID 2>/dev/null || true
        wait $MONITOR_PID 2>/dev/null || true
    fi
}

# Set up cleanup trap
trap cleanup EXIT

log "🔍 COMPREHENSIVE WORKLOAD MONITOR AUDIT"

# Create logs directory
mkdir -p logs
log_file="logs/workload-monitor-audit.log"

# Check kubectl access
log "Checking kubectl access..."
if ! kubectl cluster-info >/dev/null 2>&1; then
    error "Cannot access Kubernetes cluster"
fi
success "Kubernetes access verified"

# Build workload monitor
log "Building workload monitor..."
cd ..
go build -o workload-monitor examples/workload-monitor.go
cd e2e
success "Workload monitor built"

# STEP 1: GENERATE EXPECTED EVENTS FROM CONFIG + MANIFESTS
log "📋 STEP 1: Generating expected events from configuration and manifests..."

# Workload Monitor Configuration
WORKLOAD_LABEL="test-label"
WORKLOAD_PATTERN="faro-namespace"
WORKLOAD_ID_PATTERN="faro-test-(.*)"
CLUSTER_GVRS="v1/namespaces"
NAMESPACE_GVRS="v1/configmaps,v1/services,v1/secrets"

info "Configuration:"
info "  - Workload Label: $WORKLOAD_LABEL"
info "  - Workload Pattern: $WORKLOAD_PATTERN"
info "  - Workload ID Pattern: $WORKLOAD_ID_PATTERN"
info "  - Cluster GVRs: $CLUSTER_GVRS"
info "  - Namespace GVRs: $NAMESPACE_GVRS"

# Parse manifests to extract resources
log "Parsing manifests/unified-test-resources.yaml..."

# Extract namespaces with the workload label
expected_namespaces=$(yq eval 'select(.kind == "Namespace" and .metadata.labels."test-label" == "faro-namespace") | .metadata.name' manifests/unified-test-resources.yaml)
info "Expected workload namespaces: $(echo $expected_namespaces | tr '\n' ' ')"

# Extract workload IDs from namespace names using the pattern
expected_workload_ids=""
for ns in $expected_namespaces; do
    if [[ $ns =~ faro-test-(.*) ]]; then
        workload_id="${BASH_REMATCH[1]}"
        expected_workload_ids="$expected_workload_ids $workload_id"
        info "Namespace $ns -> Workload ID: $workload_id"
    fi
done

# Extract resources from each monitored namespace
expected_configmaps=$(yq eval 'select(.kind == "ConfigMap") | .metadata.namespace + "/" + .metadata.name' manifests/unified-test-resources.yaml | grep -E "^($(echo $expected_namespaces | tr ' ' '|'))/" || true)
expected_services=$(yq eval 'select(.kind == "Service") | .metadata.namespace + "/" + .metadata.name' manifests/unified-test-resources.yaml | grep -E "^($(echo $expected_namespaces | tr ' ' '|'))/" || true)
expected_secrets=$(yq eval 'select(.kind == "Secret") | .metadata.namespace + "/" + .metadata.name' manifests/unified-test-resources.yaml | grep -E "^($(echo $expected_namespaces | tr ' ' '|'))/" || true)

info "Expected ConfigMaps: $(echo $expected_configmaps | tr '\n' ' ')"
info "Expected Services: $(echo $expected_services | tr '\n' ' ')"
info "Expected Secrets: $(echo $expected_secrets | tr '\n' ' ')"

# Generate expected events list
log "📝 Generating expected events list..."
expected_events_file="/tmp/expected-workload-events.json"
cat > "$expected_events_file" << EOF
{
  "cluster_gvr_events": {
    "v1/namespaces": {
      "ADDED": [$(echo $expected_namespaces | sed 's/ /", "/g' | sed 's/^/"/; s/$/"/')],
      "UPDATED": [$(echo $expected_namespaces | sed 's/ /", "/g' | sed 's/^/"/; s/$/"/')],
      "DELETED": [$(echo $expected_namespaces | sed 's/ /", "/g' | sed 's/^/"/; s/$/"/')],
    }
  },
  "namespace_gvr_events": {
    "v1/configmaps": {
      "ADDED": [$(echo $expected_configmaps | sed 's/ /", "/g' | sed 's/^/"/; s/$/"/')],
      "UPDATED": [$(echo $expected_configmaps | sed 's/ /", "/g' | sed 's/^/"/; s/$/"/')],
      "DELETED": [$(echo $expected_configmaps | sed 's/ /", "/g' | sed 's/^/"/; s/$/"/')],
    },
    "v1/services": {
      "ADDED": [$(echo $expected_services | sed 's/ /", "/g' | sed 's/^/"/; s/$/"/')],
      "UPDATED": [$(echo $expected_services | sed 's/ /", "/g' | sed 's/^/"/; s/$/"/')],
      "DELETED": [$(echo $expected_services | sed 's/ /", "/g' | sed 's/^/"/; s/$/"/')],
    },
    "v1/secrets": {
      "ADDED": [$(echo $expected_secrets | sed 's/ /", "/g' | sed 's/^/"/; s/$/"/')],
      "UPDATED": [$(echo $expected_secrets | sed 's/ /", "/g' | sed 's/^/"/; s/$/"/')],
      "DELETED": [$(echo $expected_secrets | sed 's/ /", "/g' | sed 's/^/"/; s/$/"/')],
    }
  }
}
EOF

info "Expected events saved to: $expected_events_file"

# STEP 2: RUN WORKLOAD MONITOR
log "🚀 STEP 2: Starting workload monitor..."

../workload-monitor \
    -workload-label "$WORKLOAD_LABEL" \
    -workload-pattern "$WORKLOAD_PATTERN" \
    -workload-id-pattern "$WORKLOAD_ID_PATTERN" \
    -clustergvrs "$CLUSTER_GVRS" \
    -namespacegvrs "$NAMESPACE_GVRS" \
    > "$log_file" 2>&1 &

MONITOR_PID=$!
log "Workload monitor started (PID: $MONITOR_PID)"

# Wait for initialization
log "Waiting for workload monitor initialization..."
for i in {1..60}; do
    if grep -q "Ready for workload detection" "$log_file"; then
        success "Workload monitor initialized after ${i} seconds"
        break
    fi
    if [ $i -eq 60 ]; then
        error "Workload monitor initialization timeout"
    fi
    sleep 1
done

# STEP 3: RUN ALL ACTIONS
log "📦 STEP 3: Running all test actions..."

# Apply manifests (ADDED events)
log "Applying manifests (generating ADDED events)..."
kubectl apply -f manifests/unified-test-resources.yaml
sleep 5

# Re-apply manifests (UPDATED events)
log "Re-applying manifests (generating UPDATED events)..."
kubectl apply -f manifests/unified-test-resources.yaml
sleep 5

# Delete manifests (DELETED events)
log "Deleting manifests (generating DELETED events)..."
kubectl delete -f manifests/unified-test-resources.yaml
sleep 3

# STEP 4: STOP WORKLOAD MONITOR
log "🛑 STEP 4: Stopping workload monitor..."
kill $MONITOR_PID 2>/dev/null || true
wait $MONITOR_PID 2>/dev/null || true

# STEP 5: EXTRACT ACTUAL EVENTS
log "📊 STEP 5: Extracting actual events from logs..."

# Extract JSON events using the exact sed command the user specified
actual_events_file="/tmp/actual-workload-events.json"
sed -n 's/.*\[workload-handler\] \({.*}\).*/\1/p' "$log_file" > "$actual_events_file"

log "Actual JSON events extracted to: $actual_events_file"
log "📄 Actual events captured:"
echo "----------------------------------------"
cat "$actual_events_file" | jq . 2>/dev/null || cat "$actual_events_file"
echo "----------------------------------------"

# Count events by type
added_count=$(grep -c '"action":"ADDED"' "$actual_events_file" || echo "0")
updated_count=$(grep -c '"action":"UPDATED"' "$actual_events_file" || echo "0")
deleted_count=$(grep -c '"action":"DELETED"' "$actual_events_file" || echo "0")

info "Event counts: ADDED=$added_count, UPDATED=$updated_count, DELETED=$deleted_count"

# STEP 6: COMPARE EXPECTED VS ACTUAL
log "🔍 STEP 6: Comparing expected vs actual events..."

# Check each expected resource
total_expected=0
total_found=0

for ns in $expected_namespaces; do
    info "Checking namespace: $ns"
    if grep -q "\"resource_name\":\"$ns\"" "$actual_events_file"; then
        success "  ✓ Found events for namespace $ns"
        total_found=$((total_found + 1))
    else
        error "  ✗ Missing events for namespace $ns"
    fi
    total_expected=$((total_expected + 1))
done

for cm in $expected_configmaps; do
    cm_name=$(echo $cm | cut -d'/' -f2)
    info "Checking ConfigMap: $cm_name"
    if grep -q "\"resource_name\":\"$cm_name\"" "$actual_events_file"; then
        success "  ✓ Found events for ConfigMap $cm_name"
        total_found=$((total_found + 1))
    else
        error "  ✗ Missing events for ConfigMap $cm_name"
    fi
    total_expected=$((total_expected + 1))
done

for svc in $expected_services; do
    svc_name=$(echo $svc | cut -d'/' -f2)
    info "Checking Service: $svc_name"
    if grep -q "\"resource_name\":\"$svc_name\"" "$actual_events_file"; then
        success "  ✓ Found events for Service $svc_name"
        total_found=$((total_found + 1))
    else
        error "  ✗ Missing events for Service $svc_name"
    fi
    total_expected=$((total_expected + 1))
done

for secret in $expected_secrets; do
    secret_name=$(echo $secret | cut -d'/' -f2)
    info "Checking Secret: $secret_name"
    if grep -q "\"resource_name\":\"$secret_name\"" "$actual_events_file"; then
        success "  ✓ Found events for Secret $secret_name"
        total_found=$((total_found + 1))
    else
        error "  ✗ Missing events for Secret $secret_name"
    fi
    total_expected=$((total_expected + 1))
done

# Final audit result
log "📋 FINAL AUDIT RESULT:"
if [ $total_found -eq $total_expected ]; then
    success "AUDIT PASSED: Found $total_found/$total_expected expected resources"
else
    error "AUDIT FAILED: Found only $total_found/$total_expected expected resources"
fi

# Check for DELETE events specifically
if [ $deleted_count -eq 0 ]; then
    error "CRITICAL: No DELETE events found in JSON output!"
    log "CONFIG [DELETED] messages in raw log:"
    grep "CONFIG \[DELETED\]" "$log_file" || echo "None found"
else
    success "DELETE events found: $deleted_count"
fi

log "🔍 Audit complete. Full log available at: $log_file"