#!/bin/bash

# GitHub Actions Test Log Analysis Script
# Analyzes logs and events generated during CI/CD test runs

set -e

# Parameters
LOG_LEVEL=${1:-debug}
TEST_DURATION=${2:-30}
OUTPUT_DIR=${3:-"test-analysis"}
WORKLOAD_MONITOR_PATH=${4:-"examples/workload-monitor"}

echo "=== GitHub Actions Test Log Analysis ==="
echo "Log Level: $LOG_LEVEL"
echo "Test Duration: ${TEST_DURATION}s"
echo "Output Directory: $OUTPUT_DIR"
echo "Workload Monitor: $WORKLOAD_MONITOR_PATH"
echo "GitHub Run: ${GITHUB_RUN_NUMBER:-local}"
echo "GitHub SHA: ${GITHUB_SHA:-$(git rev-parse HEAD 2>/dev/null || echo 'unknown')}"
echo ""

# Create output directory
mkdir -p "$OUTPUT_DIR"

# Check if workload monitor exists, build if needed
if [ ! -f "$WORKLOAD_MONITOR_PATH" ]; then
    echo "Building workload monitor..."
    cd examples
    go build -o workload-monitor workload-monitor.go
    cd ..
fi

# Run workload monitor with specified log level
echo "Running workload monitor with log level: $LOG_LEVEL"
cd examples
timeout "${TEST_DURATION}s" ./workload-monitor -log-level="$LOG_LEVEL" 2>&1 | tee "../$OUTPUT_DIR/workload-monitor-output.log" || true

# Copy any generated log files and events
find . -name "logs" -type d -exec cp -r {} "../$OUTPUT_DIR/" \; 2>/dev/null || true
find . -name "*.log" -type f -exec cp {} "../$OUTPUT_DIR/" \; 2>/dev/null || true
find . -name "*.json" -type f -exec cp {} "../$OUTPUT_DIR/" \; 2>/dev/null || true

cd ..

# Analyze log levels and events
echo ""
echo "=== Analysis Results ==="

DEBUG_COUNT=$(find "$OUTPUT_DIR" -name "*.log" -exec grep -h "^D[0-9]" {} \; 2>/dev/null | wc -l || echo 0)
INFO_COUNT=$(find "$OUTPUT_DIR" -name "*.log" -exec grep -h "^I[0-9]" {} \; 2>/dev/null | wc -l || echo 0)
TOTAL_COUNT=$((DEBUG_COUNT + INFO_COUNT))

# Count JSON events
EVENT_COUNT=$(find "$OUTPUT_DIR" -name "*.json" -type f -exec wc -l {} \; 2>/dev/null | awk '{sum+=$1} END {print sum+0}')
EVENT_FILES=$(find "$OUTPUT_DIR" -name "*.json" -type f | wc -l)

echo "Debug messages (^D): $DEBUG_COUNT"
echo "Info messages (^I): $INFO_COUNT"
echo "Total log messages: $TOTAL_COUNT"
echo "JSON event files: $EVENT_FILES"
echo "Total JSON events: $EVENT_COUNT"

# Calculate percentages
if [ $TOTAL_COUNT -gt 0 ]; then
    DEBUG_PERCENT=$(echo "scale=1; $DEBUG_COUNT * 100 / $TOTAL_COUNT" | bc -l 2>/dev/null || echo "0.0")
    INFO_PERCENT=$(echo "scale=1; $INFO_COUNT * 100 / $TOTAL_COUNT" | bc -l 2>/dev/null || echo "0.0")
    echo "Debug percentage: ${DEBUG_PERCENT}%"
    echo "Info percentage: ${INFO_PERCENT}%"
    
    # Validation
    if [ "$LOG_LEVEL" = "info" ] && [ "$DEBUG_COUNT" -eq 0 ]; then
        echo "✅ PASS: No debug messages when log-level=info"
        VALIDATION_STATUS="PASS"
    elif [ "$LOG_LEVEL" = "debug" ] && [ "$DEBUG_COUNT" -gt 0 ]; then
        echo "✅ PASS: Debug messages present when log-level=debug"
        VALIDATION_STATUS="PASS"
    else
        echo "❌ FAIL: Unexpected log level behavior"
        VALIDATION_STATUS="FAIL"
    fi
else
    VALIDATION_STATUS="NO_LOGS"
fi

# Analyze event types if events exist
if [ "$EVENT_COUNT" -gt 0 ]; then
    echo ""
    echo "=== Event Type Analysis ==="
    find "$OUTPUT_DIR" -name "*.json" -type f -exec grep -h '"eventType"' {} \; 2>/dev/null | sort | uniq -c | sort -nr || true
fi

# Create comprehensive analysis report for GitHub Actions
cat > "$OUTPUT_DIR/github-actions-analysis.json" << EOF
{
  "metadata": {
    "github_run_number": "${GITHUB_RUN_NUMBER:-null}",
    "github_sha": "${GITHUB_SHA:-null}",
    "github_ref": "${GITHUB_REF:-null}",
    "log_level": "$LOG_LEVEL",
    "test_duration_seconds": $TEST_DURATION,
    "generated_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
    "validation_status": "$VALIDATION_STATUS"
  },
  "log_analysis": {
    "debug_messages": $DEBUG_COUNT,
    "info_messages": $INFO_COUNT,
    "total_messages": $TOTAL_COUNT,
    "debug_percentage": ${DEBUG_PERCENT:-0.0},
    "info_percentage": ${INFO_PERCENT:-0.0}
  },
  "event_analysis": {
    "event_files": $EVENT_FILES,
    "total_events": $EVENT_COUNT
  }
}
EOF

# Create human-readable report
cat > "$OUTPUT_DIR/analysis-report.md" << EOF
# Test Log Analysis Report

## Test Configuration
- **Log Level**: $LOG_LEVEL
- **Test Duration**: ${TEST_DURATION}s
- **GitHub Run**: ${GITHUB_RUN_NUMBER:-local}
- **Commit SHA**: ${GITHUB_SHA:-$(git rev-parse HEAD 2>/dev/null || echo 'unknown')}
- **Generated**: $(date)

## Log Message Analysis
- **Debug messages (^D)**: $DEBUG_COUNT
- **Info messages (^I)**: $INFO_COUNT
- **Total messages**: $TOTAL_COUNT

EOF

if [ $TOTAL_COUNT -gt 0 ]; then
    cat >> "$OUTPUT_DIR/analysis-report.md" << EOF
- **Debug percentage**: ${DEBUG_PERCENT}%
- **Info percentage**: ${INFO_PERCENT}%

## Validation Results
- **Status**: $VALIDATION_STATUS
EOF
    
    if [ "$VALIDATION_STATUS" = "PASS" ]; then
        echo "- **Result**: ✅ Logging levels working correctly" >> "$OUTPUT_DIR/analysis-report.md"
    else
        echo "- **Result**: ❌ Logging level validation failed" >> "$OUTPUT_DIR/analysis-report.md"
    fi
fi

cat >> "$OUTPUT_DIR/analysis-report.md" << EOF

## Event Analysis
- **JSON event files**: $EVENT_FILES
- **Total JSON events**: $EVENT_COUNT

EOF

# Add event type analysis if events exist
if [ "$EVENT_COUNT" -gt 0 ]; then
    echo "## Event Type Distribution" >> "$OUTPUT_DIR/analysis-report.md"
    echo '```' >> "$OUTPUT_DIR/analysis-report.md"
    find "$OUTPUT_DIR" -name "*.json" -type f -exec grep -h '"eventType"' {} \; 2>/dev/null | sort | uniq -c | sort -nr >> "$OUTPUT_DIR/analysis-report.md" || true
    echo '```' >> "$OUTPUT_DIR/analysis-report.md"
fi

cat >> "$OUTPUT_DIR/analysis-report.md" << EOF

## Files Generated
\`\`\`
$(ls -la "$OUTPUT_DIR")
\`\`\`

## Usage in GitHub Actions
This analysis was generated by \`.github/scripts/analyze-test-logs.sh\` and can be used in workflows to:
- Validate logging level behavior
- Monitor event generation patterns
- Provide detailed test artifacts for debugging

EOF

echo ""
echo "=== Analysis Complete ==="
echo "JSON report: $OUTPUT_DIR/github-actions-analysis.json"
echo "Markdown report: $OUTPUT_DIR/analysis-report.md"
echo "Validation status: $VALIDATION_STATUS"

# Set GitHub Actions outputs if running in CI
if [ -n "$GITHUB_OUTPUT" ]; then
    echo "debug_count=$DEBUG_COUNT" >> "$GITHUB_OUTPUT"
    echo "info_count=$INFO_COUNT" >> "$GITHUB_OUTPUT"
    echo "total_count=$TOTAL_COUNT" >> "$GITHUB_OUTPUT"
    echo "event_count=$EVENT_COUNT" >> "$GITHUB_OUTPUT"
    echo "validation_status=$VALIDATION_STATUS" >> "$GITHUB_OUTPUT"
fi

# Exit with error code if validation failed
if [ "$VALIDATION_STATUS" = "FAIL" ]; then
    echo "❌ Validation failed - exiting with error code 1"
    exit 1
fi

echo "✅ Analysis completed successfully"