#!/bin/bash

# Universal E2E Test Auditor
# This script can audit any Faro e2e test by parsing config + manifests and comparing expected vs actual events

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
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

audit_header() {
    echo -e "${PURPLE}[$(date +'%Y-%m-%d %H:%M:%S')] 🔍 $1${NC}"
}

cleanup() {
    log "Cleanup: Removing test resources..."
    kubectl delete -f manifests/unified-test-resources.yaml --ignore-not-found=true || true
    
    # Kill Faro if it's still running
    if [[ -n "${FARO_PID:-}" ]]; then
        log "Cleanup: Stopping Faro (PID: $FARO_PID)..."
        kill $FARO_PID 2>/dev/null || true
        wait $FARO_PID 2>/dev/null || true
    fi
}

# Set up cleanup trap
trap cleanup EXIT

# Usage function
usage() {
    echo "Usage: $0 <test_number> [config_file] [manifest_file]"
    echo ""
    echo "Examples:"
    echo "  $0 1                                    # Audit test 1 with default config and manifest"
    echo "  $0 3 configs/simple-test-3.yaml        # Audit test 3 with custom config"
    echo "  $0 5 configs/simple-test-5.yaml manifests/custom.yaml  # Custom config and manifest"
    echo ""
    echo "Available tests: 1-10"
    exit 1
}

# Parse command line arguments
if [ $# -lt 1 ]; then
    usage
fi

TEST_NUMBER="$1"
CONFIG_FILE="${2:-configs/simple-test-${TEST_NUMBER}.yaml}"
MANIFEST_FILE="${3:-manifests/unified-test-resources.yaml}"

# Validate test number
if [[ ! "$TEST_NUMBER" =~ ^[1-9]|10$ ]]; then
    error "Invalid test number: $TEST_NUMBER. Must be 1-10."
fi

# Validate files exist
if [[ ! -f "$CONFIG_FILE" ]]; then
    error "Config file not found: $CONFIG_FILE"
fi

if [[ ! -f "$MANIFEST_FILE" ]]; then
    error "Manifest file not found: $MANIFEST_FILE"
fi

audit_header "UNIVERSAL E2E TEST AUDIT - TEST $TEST_NUMBER"

# Create logs directory
mkdir -p logs
log_file="logs/universal-audit-test${TEST_NUMBER}.log"

# Check kubectl access
log "Checking kubectl access..."
if ! kubectl cluster-info >/dev/null 2>&1; then
    error "Cannot access Kubernetes cluster"
fi
success "Kubernetes access verified"

# Build Faro
log "Building Faro..."
cd ..
go build -o faro .
cd e2e
success "Faro built"

# STEP 1: PARSE CONFIG AND GENERATE EXPECTED EVENTS
audit_header "STEP 1: Parsing config and generating expected events"

info "Config file: $CONFIG_FILE"
info "Manifest file: $MANIFEST_FILE"

# Parse the config file to extract monitoring rules
log "Parsing Faro config: $CONFIG_FILE"

# Extract namespace patterns from config
namespace_patterns=""
resource_configs=""

# Check if config uses namespace-centric approach
if yq eval '.namespaces' "$CONFIG_FILE" | grep -q "name_pattern"; then
    info "Config type: Namespace-centric"
    namespace_patterns=$(yq eval '.namespaces[].name_pattern' "$CONFIG_FILE" | tr '\n' ' ')
    
    # Extract resource configs from namespace sections
    while IFS= read -r ns_pattern; do
        info "  Namespace pattern: $ns_pattern"
        
        # Get resources for this namespace pattern
        ns_index=0
        while yq eval ".namespaces[$ns_index].name_pattern" "$CONFIG_FILE" 2>/dev/null | grep -q "$ns_pattern"; do
            resources=$(yq eval ".namespaces[$ns_index].resources | keys" "$CONFIG_FILE" 2>/dev/null | grep -v "^null$" | sed 's/^- //')
            for resource in $resources; do
                if [[ -n "$resource" && "$resource" != "null" ]]; then
                    name_pattern=$(yq eval ".namespaces[$ns_index].resources.\"$resource\".name_pattern" "$CONFIG_FILE" 2>/dev/null || echo ".*")
                    label_selector=$(yq eval ".namespaces[$ns_index].resources.\"$resource\".label_selector" "$CONFIG_FILE" 2>/dev/null || echo "")
                    
                    resource_configs="$resource_configs\n$resource|$ns_pattern|$name_pattern|$label_selector"
                    info "    Resource: $resource, Name pattern: $name_pattern, Label selector: $label_selector"
                fi
            done
            ns_index=$((ns_index + 1))
        done
    done <<< "$(echo "$namespace_patterns" | tr ' ' '\n' | grep -v '^$')"
    
elif yq eval '.resources' "$CONFIG_FILE" | grep -q "gvr"; then
    info "Config type: Resource-centric"
    
    # Extract resource configs from resource sections
    resource_index=0
    while yq eval ".resources[$resource_index].gvr" "$CONFIG_FILE" 2>/dev/null | grep -v "^null$" >/dev/null; do
        gvr=$(yq eval ".resources[$resource_index].gvr" "$CONFIG_FILE")
        scope=$(yq eval ".resources[$resource_index].scope" "$CONFIG_FILE" 2>/dev/null || echo "Namespaced")
        ns_patterns=$(yq eval ".resources[$resource_index].namespace_patterns[]" "$CONFIG_FILE" 2>/dev/null | tr '\n' ',' | sed 's/,$//')
        name_pattern=$(yq eval ".resources[$resource_index].name_pattern" "$CONFIG_FILE" 2>/dev/null || echo ".*")
        label_selector=$(yq eval ".resources[$resource_index].label_selector" "$CONFIG_FILE" 2>/dev/null || echo "")
        
        resource_configs="$resource_configs\n$gvr|$ns_patterns|$name_pattern|$label_selector"
        info "  Resource: $gvr, Scope: $scope, Namespaces: $ns_patterns, Name pattern: $name_pattern, Label selector: $label_selector"
        
        resource_index=$((resource_index + 1))
    done
else
    error "Unknown config format in $CONFIG_FILE"
fi

# Parse manifests to extract actual resources
log "Parsing manifest file: $MANIFEST_FILE"

# Extract all resources from manifest
all_namespaces=$(yq eval 'select(.kind == "Namespace") | .metadata.name' "$MANIFEST_FILE" | grep -v "^---$" || true)
all_configmaps=$(yq eval 'select(.kind == "ConfigMap") | .metadata.namespace + "/" + .metadata.name' "$MANIFEST_FILE" | grep -v "^---$" || true)
all_services=$(yq eval 'select(.kind == "Service") | .metadata.namespace + "/" + .metadata.name' "$MANIFEST_FILE" | grep -v "^---$" || true)
all_secrets=$(yq eval 'select(.kind == "Secret") | .metadata.namespace + "/" + .metadata.name' "$MANIFEST_FILE" | grep -v "^---$" || true)

info "Manifest resources found:"
info "  Namespaces: $(echo $all_namespaces | tr '\n' ' ')"
info "  ConfigMaps: $(echo $all_configmaps | tr '\n' ' ')"
info "  Services: $(echo $all_services | tr '\n' ' ')"
info "  Secrets: $(echo $all_secrets | tr '\n' ' ')"

# Generate expected events based on config + manifest intersection
log "Generating expected events matrix..."

expected_events_file="/tmp/expected-events-test${TEST_NUMBER}.json"
actual_events_file="/tmp/actual-events-test${TEST_NUMBER}.json"

# Create expected events structure
cat > "$expected_events_file" << 'EOF'
{
  "expected_resources": [],
  "event_matrix": {
    "ADDED": [],
    "UPDATED": [],
    "DELETED": []
  }
}
EOF

# Apply filtering logic to determine which resources should be captured
expected_resources=""

# Process each resource config
echo -e "$resource_configs" | grep -v "^$" | while IFS='|' read -r gvr ns_pattern name_pattern label_selector; do
    if [[ -n "$gvr" ]]; then
        info "Processing config: GVR=$gvr, NS_Pattern=$ns_pattern, Name_Pattern=$name_pattern, Label_Selector=$label_selector"
        
        case "$gvr" in
            "v1/namespaces")
                # Match namespaces against pattern
                for ns in $all_namespaces; do
                    if [[ "$ns" =~ $ns_pattern ]]; then
                        expected_resources="$expected_resources $gvr:$ns"
                        info "  Expected: $gvr:$ns"
                    fi
                done
                ;;
            "v1/configmaps")
                # Match configmaps against namespace and name patterns
                for cm in $all_configmaps; do
                    cm_ns=$(echo "$cm" | cut -d'/' -f1)
                    cm_name=$(echo "$cm" | cut -d'/' -f2)
                    
                    # Check namespace pattern
                    if [[ "$cm_ns" =~ $ns_pattern ]]; then
                        # Check name pattern
                        if [[ "$cm_name" =~ $name_pattern ]]; then
                            # TODO: Check label selector if specified
                            expected_resources="$expected_resources $gvr:$cm"
                            info "  Expected: $gvr:$cm"
                        fi
                    fi
                done
                ;;
            "v1/services")
                # Match services against namespace and name patterns
                for svc in $all_services; do
                    svc_ns=$(echo "$svc" | cut -d'/' -f1)
                    svc_name=$(echo "$svc" | cut -d'/' -f2)
                    
                    # Check namespace pattern
                    if [[ "$svc_ns" =~ $ns_pattern ]]; then
                        # Check name pattern
                        if [[ "$svc_name" =~ $name_pattern ]]; then
                            expected_resources="$expected_resources $gvr:$svc"
                            info "  Expected: $gvr:$svc"
                        fi
                    fi
                done
                ;;
            "v1/secrets")
                # Match secrets against namespace and name patterns
                for secret in $all_secrets; do
                    secret_ns=$(echo "$secret" | cut -d'/' -f1)
                    secret_name=$(echo "$secret" | cut -d'/' -f2)
                    
                    # Check namespace pattern
                    if [[ "$secret_ns" =~ $ns_pattern ]]; then
                        # Check name pattern
                        if [[ "$secret_name" =~ $name_pattern ]]; then
                            expected_resources="$expected_resources $gvr:$secret"
                            info "  Expected: $gvr:$secret"
                        fi
                    fi
                done
                ;;
        esac
    fi
done

# Save expected resources for later comparison
echo "$expected_resources" > "/tmp/expected-resources-test${TEST_NUMBER}.txt"

# STEP 2: RUN FARO WITH CONFIG
audit_header "STEP 2: Starting Faro with config"

../faro -config "$CONFIG_FILE" > "$log_file" 2>&1 &
FARO_PID=$!
log "Faro started (PID: $FARO_PID)"

# Wait for Faro initialization
log "Waiting for Faro initialization..."
for i in {1..30}; do
    if grep -q "Starting config-driven informers" "$log_file" 2>/dev/null; then
        success "Faro initialized after ${i} seconds"
        break
    fi
    if [ $i -eq 30 ]; then
        error "Faro initialization timeout"
    fi
    sleep 1
done

# STEP 3: RUN ALL ACTIONS
audit_header "STEP 3: Running all test actions"

# Apply manifests (ADDED events)
log "Applying manifests (generating ADDED events)..."
kubectl apply -f "$MANIFEST_FILE"
sleep 5

# Re-apply manifests (UPDATED events)
log "Re-applying manifests (generating UPDATED events)..."
kubectl apply -f "$MANIFEST_FILE"
sleep 5

# Delete manifests (DELETED events)
log "Deleting manifests (generating DELETED events)..."
kubectl delete -f "$MANIFEST_FILE"
sleep 3

# STEP 4: STOP FARO
audit_header "STEP 4: Stopping Faro"
kill $FARO_PID 2>/dev/null || true
wait $FARO_PID 2>/dev/null || true

# STEP 5: EXTRACT AND COMPARE EVENTS
audit_header "STEP 5: Extracting and comparing events"

# Check if Faro generates JSON events (like workload-monitor) or just CONFIG messages
if grep -q '{"timestamp"' "$log_file"; then
    log "Faro generates JSON events - extracting with jq..."
    grep '{"timestamp"' "$log_file" | jq . > "$actual_events_file" 2>/dev/null || {
        log "JSON parsing failed, extracting raw JSON lines..."
        grep '{"timestamp"' "$log_file" > "$actual_events_file"
    }
else
    log "Faro generates CONFIG messages only - no JSON events to extract"
    echo "[]" > "$actual_events_file"
fi

# Count events
if [[ -s "$actual_events_file" ]]; then
    added_count=$(grep -c '"action":"ADDED"' "$actual_events_file" 2>/dev/null || grep -c '"eventType":"ADDED"' "$actual_events_file" 2>/dev/null || echo "0")
    updated_count=$(grep -c '"action":"UPDATED"' "$actual_events_file" 2>/dev/null || grep -c '"eventType":"UPDATED"' "$actual_events_file" 2>/dev/null || echo "0")
    deleted_count=$(grep -c '"action":"DELETED"' "$actual_events_file" 2>/dev/null || grep -c '"eventType":"DELETED"' "$actual_events_file" 2>/dev/null || echo "0")
else
    added_count=0
    updated_count=0
    deleted_count=0
fi

# Count CONFIG messages as fallback
config_added=$(grep -c "CONFIG \[ADDED\]" "$log_file" || echo "0")
config_updated=$(grep -c "CONFIG \[UPDATED\]" "$log_file" || echo "0")
config_deleted=$(grep -c "CONFIG \[DELETED\]" "$log_file" || echo "0")

info "Event counts:"
info "  JSON Events: ADDED=$added_count, UPDATED=$updated_count, DELETED=$deleted_count"
info "  CONFIG Messages: ADDED=$config_added, UPDATED=$config_updated, DELETED=$config_deleted"

# STEP 6: FINAL AUDIT RESULT
audit_header "STEP 6: Final audit result"

# Read expected resources
expected_resources=$(cat "/tmp/expected-resources-test${TEST_NUMBER}.txt" 2>/dev/null || echo "")
expected_count=$(echo "$expected_resources" | wc -w)

if [[ $expected_count -eq 0 ]]; then
    error "No expected resources generated - check config and manifest compatibility"
fi

info "Expected $expected_count resources based on config filtering"

# Check if we have any events at all
total_json_events=$((added_count + updated_count + deleted_count))
total_config_events=$((config_added + config_updated + config_deleted))

if [[ $total_json_events -gt 0 ]]; then
    success "Test $TEST_NUMBER: Found $total_json_events JSON events"
    
    # TODO: Detailed resource-by-resource comparison
    log "Detailed comparison not yet implemented - manual review required"
    log "Expected resources: $expected_resources"
    log "Actual events file: $actual_events_file"
    log "Full log file: $log_file"
    
elif [[ $total_config_events -gt 0 ]]; then
    log "Test $TEST_NUMBER: Found $total_config_events CONFIG events (no JSON)"
    log "This test uses vanilla Faro (CONFIG messages only)"
    
    # For CONFIG-only tests, verify expected resources appear in CONFIG messages
    success_count=0
    for resource in $expected_resources; do
        gvr=$(echo "$resource" | cut -d':' -f1)
        name=$(echo "$resource" | cut -d':' -f2)
        
        if grep -q "CONFIG.*$gvr.*$name" "$log_file"; then
            success "  ✓ Found CONFIG events for $resource"
            success_count=$((success_count + 1))
        else
            error "  ✗ Missing CONFIG events for $resource"
        fi
    done
    
    if [[ $success_count -eq $expected_count ]]; then
        success "Test $TEST_NUMBER PASSED: All $expected_count expected resources found in CONFIG messages"
    else
        error "Test $TEST_NUMBER FAILED: Only $success_count/$expected_count expected resources found"
    fi
    
else
    error "Test $TEST_NUMBER FAILED: No events found at all!"
fi

log "🔍 Audit complete for Test $TEST_NUMBER"
log "Full log: $log_file"
log "Expected resources: /tmp/expected-resources-test${TEST_NUMBER}.txt"
log "Actual events: $actual_events_file"