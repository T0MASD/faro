#!/bin/bash

# Test Log Analysis Script
# Simulates the log analysis done in GitHub Actions workflows

set -e

LOG_LEVEL=${1:-debug}
TEST_DURATION=${2:-10}
OUTPUT_DIR="local-test-logs"

echo "=== Local Test Log Analysis ==="
echo "Log Level: $LOG_LEVEL"
echo "Test Duration: ${TEST_DURATION}s"
echo "Output Directory: $OUTPUT_DIR"
echo ""

# Create output directory
mkdir -p "$OUTPUT_DIR"

# Build workload monitor if it doesn't exist
if [ ! -f "examples/workload-monitor" ]; then
    echo "Building workload monitor..."
    cd examples
    go build -o workload-monitor workload-monitor.go
    cd ..
fi

# Run workload monitor with specified log level
echo "Running workload monitor with log level: $LOG_LEVEL"
cd examples
timeout "${TEST_DURATION}s" ./workload-monitor -log-level="$LOG_LEVEL" 2>&1 | tee "../$OUTPUT_DIR/workload-monitor-output.log" || true

# Copy any generated log files
find . -name "logs" -type d -exec cp -r {} "../$OUTPUT_DIR/" \; 2>/dev/null || true
find . -name "*.log" -type f -exec cp {} "../$OUTPUT_DIR/" \; 2>/dev/null || true
find . -name "*.json" -type f -exec cp {} "../$OUTPUT_DIR/" \; 2>/dev/null || true

cd ..

# Analyze log levels and events
echo ""
echo "=== Log Level Analysis ==="

DEBUG_COUNT=$(find "$OUTPUT_DIR" -name "*.log" -exec grep -h "^D[0-9]" {} \; 2>/dev/null | wc -l || echo 0)
INFO_COUNT=$(find "$OUTPUT_DIR" -name "*.log" -exec grep -h "^I[0-9]" {} \; 2>/dev/null | wc -l || echo 0)
TOTAL_COUNT=$((DEBUG_COUNT + INFO_COUNT))

# Count JSON events
EVENT_COUNT=$(find "$OUTPUT_DIR" -name "*.json" -type f -exec wc -l {} \; 2>/dev/null | awk '{sum+=$1} END {print sum+0}')
EVENT_FILES=$(find "$OUTPUT_DIR" -name "*.json" -type f | wc -l)

echo "Debug messages (^D): $DEBUG_COUNT"
echo "Info messages (^I): $INFO_COUNT"
echo "Total messages: $TOTAL_COUNT"
echo "JSON event files: $EVENT_FILES"
echo "Total JSON events: $EVENT_COUNT"

if [ $TOTAL_COUNT -gt 0 ]; then
    DEBUG_PERCENT=$(echo "scale=1; $DEBUG_COUNT * 100 / $TOTAL_COUNT" | bc -l 2>/dev/null || echo "0.0")
    INFO_PERCENT=$(echo "scale=1; $INFO_COUNT * 100 / $TOTAL_COUNT" | bc -l 2>/dev/null || echo "0.0")
    echo "Debug percentage: ${DEBUG_PERCENT}%"
    echo "Info percentage: ${INFO_PERCENT}%"
    
    if [ "$LOG_LEVEL" = "info" ] && [ "$DEBUG_COUNT" -eq 0 ]; then
        echo "✅ PASS: No debug messages when log-level=info"
    elif [ "$LOG_LEVEL" = "debug" ] && [ "$DEBUG_COUNT" -gt 0 ]; then
        echo "✅ PASS: Debug messages present when log-level=debug"
    else
        echo "❌ FAIL: Unexpected log level behavior"
    fi
fi

# Analyze event types if events exist
if [ "$EVENT_COUNT" -gt 0 ]; then
    echo ""
    echo "=== Event Analysis ==="
    echo "Event type distribution:"
    find "$OUTPUT_DIR" -name "*.json" -type f -exec grep -h '"eventType"' {} \; 2>/dev/null | sort | uniq -c | sort -nr || true
fi

# Create analysis report
cat > "$OUTPUT_DIR/analysis-report.txt" << EOF
=== Local Test Log Analysis Report ===
Test Configuration:
- Log Level: $LOG_LEVEL
- Test Duration: ${TEST_DURATION}s
- Generated at: $(date)
- Working Directory: $(pwd)

Log Message Counts:
- Debug messages (^D): $DEBUG_COUNT
- Info messages (^I): $INFO_COUNT
- Total messages: $TOTAL_COUNT

Event Counts:
- JSON event files: $EVENT_FILES
- Total JSON events: $EVENT_COUNT

EOF

if [ $TOTAL_COUNT -gt 0 ]; then
    cat >> "$OUTPUT_DIR/analysis-report.txt" << EOF
Percentages:
- Debug: ${DEBUG_PERCENT}%
- Info: ${INFO_PERCENT}%

EOF
fi

# Add event analysis to report if events exist
if [ "$EVENT_COUNT" -gt 0 ]; then
    cat >> "$OUTPUT_DIR/analysis-report.txt" << EOF

Event Type Analysis:
EOF
    find "$OUTPUT_DIR" -name "*.json" -type f -exec grep -h '"eventType"' {} \; 2>/dev/null | sort | uniq -c | sort -nr >> "$OUTPUT_DIR/analysis-report.txt" || true
fi

echo "" >> "$OUTPUT_DIR/analysis-report.txt"
echo "Files generated:" >> "$OUTPUT_DIR/analysis-report.txt"
ls -la "$OUTPUT_DIR" >> "$OUTPUT_DIR/analysis-report.txt"

echo ""
echo "=== Files Generated ==="
ls -la "$OUTPUT_DIR"

echo ""
echo "=== Analysis Complete ==="
echo "Results saved to: $OUTPUT_DIR/analysis-report.txt"
echo ""
echo "To simulate GitHub Actions artifact upload locally:"
echo "  tar -czf test-logs-$LOG_LEVEL-$(date +%s).tar.gz $OUTPUT_DIR"
echo ""
echo "To test different log levels:"
echo "  $0 info 15    # Test info level for 15 seconds"
echo "  $0 debug 30   # Test debug level for 30 seconds"