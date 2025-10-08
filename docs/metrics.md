# Faro Metrics

## Overview

Faro provides **simple Prometheus metrics** for monitoring core library mechanisms - informer lifecycle, event processing, and resource tracking. Metrics focus on **library internals**, not business logic.

## Philosophy: Mechanism Metrics Only

**Faro Core Provides**: Metrics for informer management, event streaming, and JSON export
**Library Users Implement**: Business logic metrics (workload detection, CRD discovery, etc.)

## Configuration

Enable metrics in your Faro configuration:

```yaml
metrics:
  enabled: true          # Enable Prometheus metrics collection
  port: 8080            # HTTP server port for metrics endpoint
  path: "/metrics"      # Metrics endpoint path (default: /metrics)
  bind_addr: "0.0.0.0"  # Bind address (default: 0.0.0.0)
```

## Programmatic Usage

```go
// Create metrics collector
metricsConfig := faro.MetricsConfig{
    Enabled:  true,
    Port:     8080,
    Path:     "/metrics",
    BindAddr: "0.0.0.0",
}

metricsCollector := faro.NewMetricsCollector(metricsConfig, logger)

// Metrics collector automatically integrates with controller
// No manual registration required - controller calls metrics hooks internally

// Shutdown gracefully
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
metricsCollector.Shutdown(ctx)
```

## Metrics Endpoints

- **Metrics**: `http://localhost:8080/metrics`
- **Health**: `http://localhost:8080/health`
- **Readiness**: `http://localhost:8080/ready`

## Core Library Metrics

### Informer Lifecycle Metrics

#### `faro_informers_total`
**Type**: Gauge  
**Description**: Total number of active informers by status  
**Labels**:
- `status`: Informer status (`active`, `syncing`, `failed`)

```promql
# Active informers
faro_informers_total{status="active"}

# Failed informers requiring attention
faro_informers_total{status="failed"}
```

#### `faro_informer_sync_duration_seconds`
**Type**: Histogram  
**Description**: Time taken for informer initial sync completion  
**Labels**:
- `gvr`: Group/Version/Resource identifier

**Buckets**: 0.1, 0.5, 1.0, 2.0, 5.0, 10.0, 30.0, 60.0 seconds

```promql
# 95th percentile sync duration
histogram_quantile(0.95, faro_informer_sync_duration_seconds_bucket)

# Average sync time per GVR
rate(faro_informer_sync_duration_seconds_sum[5m]) / 
rate(faro_informer_sync_duration_seconds_count[5m])
```

#### `faro_informer_health`
**Type**: Gauge  
**Description**: Informer health status (1=healthy, 0=unhealthy)  
**Labels**:
- `gvr`: Group/Version/Resource identifier
- `status`: Health status (`healthy`, `sync_failed`, `stale_events`)

```promql
# Healthy informers
faro_informer_health{status="healthy"} == 1

# Informers with sync failures
faro_informer_health{status="sync_failed"} == 0
```

### Event Processing Metrics

#### `faro_events_total`
**Type**: Counter  
**Description**: Total number of events processed per GVR and type  
**Labels**:
- `gvr`: Group/Version/Resource identifier
- `event_type`: Type of event (`ADDED`, `UPDATED`, `DELETED`)

```promql
# Event rate by GVR
rate(faro_events_total[5m])

# Top 5 most active GVRs
topk(5, rate(faro_events_total[5m]))

# DELETED event rate
rate(faro_events_total{event_type="DELETED"}[5m])
```

#### `faro_informer_last_event_timestamp`
**Type**: Gauge  
**Description**: Unix timestamp of last event processed by informer  
**Labels**:
- `gvr`: Group/Version/Resource identifier

```promql
# Time since last event (seconds)
time() - faro_informer_last_event_timestamp

# Informers with no events in last 5 minutes
time() - faro_informer_last_event_timestamp > 300
```

### Resource Tracking Metrics

#### `faro_tracked_resources_total`
**Type**: Gauge  
**Description**: Number of resources currently tracked in UID cache per GVR  
**Labels**:
- `gvr`: Group/Version/Resource identifier

```promql
# Total resources tracked
sum(faro_tracked_resources_total)

# Resources tracked per GVR
faro_tracked_resources_total

# GVRs with most resources
topk(5, faro_tracked_resources_total)
```

## What Faro Core Does NOT Measure

### No Business Logic Metrics
Faro core does **not** provide metrics for:
- **CRD Discovery**: Library users implement CRD metrics if needed
- **Workload Detection**: Library users implement workload metrics if needed  
- **Event Processing Logic**: Library users implement business metrics if needed
- **External Integrations**: Library users implement integration metrics if needed

### Library User Responsibilities

Library users implement business logic metrics:

```go
// Library user implements business metrics
type WorkloadMetrics struct {
    workloadsDetected prometheus.CounterVec
    gvrsDiscovered   prometheus.CounterVec
    annotationsAdded prometheus.CounterVec
}

func (w *WorkloadMetrics) OnMatched(event faro.MatchedEvent) error {
    // Implement business logic metrics
    if w.isWorkloadNamespace(event.Object.GetNamespace()) {
        w.workloadsDetected.WithLabelValues(
            event.Object.GetNamespace(),
        ).Inc()
    }
    
    if event.GVR == "v1/events" {
        gvr := w.extractGVRFromEvent(event.Object)
        w.gvrsDiscovered.WithLabelValues(gvr).Inc()
    }
    
    return nil
}
```

## Common Queries

### Core Library Performance
```promql
# Overall event processing rate
sum(rate(faro_events_total[5m]))

# Event processing rate by type
sum by (event_type) (rate(faro_events_total[5m]))

# Slowest informer sync times
topk(5, faro_informer_sync_duration_seconds{quantile="0.95"})

# Most active GVRs by event volume
topk(10, sum by (gvr) (rate(faro_events_total[5m])))
```

### Library Health Monitoring
```promql
# Failed informers
faro_informers_total{status="failed"}

# Informers with sync issues
faro_informer_health{status="sync_failed"} == 0

# Stale informers (no events in 10 minutes)
time() - faro_informer_last_event_timestamp > 600
```

### Capacity Planning
```promql
# Total resources being tracked
sum(faro_tracked_resources_total)

# Resource growth rate
rate(faro_tracked_resources_total[1h])

# Event volume trends
increase(faro_events_total[1h])
```

## Alerting Rules

### Critical Library Alerts
```yaml
groups:
- name: faro.library.critical
  rules:
  - alert: FaroInformerDown
    expr: faro_informers_total{status="failed"} > 0
    for: 1m
    labels:
      severity: critical
    annotations:
      summary: "Faro informer failed"
      description: "{{ $value }} Faro informers are in failed state"

  - alert: FaroNoEventProcessing
    expr: rate(faro_events_total[5m]) == 0
    for: 5m
    labels:
      severity: critical
    annotations:
      summary: "Faro not processing events"
      description: "No events processed in last 5 minutes"
```

### Warning Library Alerts
```yaml
- name: faro.library.warning
  rules:
  - alert: FaroStaleInformer
    expr: time() - faro_informer_last_event_timestamp > 1800
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "Faro informer has stale events"
      description: "{{ $labels.gvr }} informer has not processed events for {{ $value }} seconds"

  - alert: FaroSlowInformerSync
    expr: histogram_quantile(0.95, faro_informer_sync_duration_seconds_bucket) > 30
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "Slow informer sync"
      description: "{{ $labels.gvr }} 95th percentile sync time is {{ $value }}s"
```

## Grafana Dashboard

### Core Library Overview Panel
```promql
# Active Informers
faro_informers_total{status="active"}

# Total Events/sec
sum(rate(faro_events_total[5m]))

# Total Resources Tracked
sum(faro_tracked_resources_total)
```

### Library Performance Panel
```promql
# Event Rate by GVR
sum by (gvr) (rate(faro_events_total[5m]))

# Sync Duration by GVR
histogram_quantile(0.95, sum by (gvr, le) (faro_informer_sync_duration_seconds_bucket))
```

### Library Health Panel
```promql
# Informer Status
faro_informers_total

# Informer Health
faro_informer_health

# Time Since Last Event
time() - faro_informer_last_event_timestamp
```

## Integration with Business Logic Metrics

### Separate Metric Namespaces
```go
// Faro core metrics (provided by library)
faro_informers_total
faro_events_total
faro_tracked_resources_total

// Business logic metrics (implemented by users)
workload_monitor_workloads_detected_total
workload_monitor_gvrs_discovered_total
crd_watcher_crds_processed_total
event_processor_annotations_added_total
```

### Combined Monitoring
```go
// Library user combines core and business metrics
type CombinedMetrics struct {
    // Core library metrics (automatic)
    faroController *faro.Controller
    
    // Business logic metrics (user-implemented)
    workloadMetrics *WorkloadMetrics
    crdMetrics      *CRDMetrics
    eventMetrics    *EventMetrics
}

func (c *CombinedMetrics) RegisterHandlers() {
    // Register business logic handlers that include metrics
    c.faroController.AddEventHandler(c.workloadMetrics)
    c.faroController.AddEventHandler(c.crdMetrics)
    c.faroController.AddEventHandler(c.eventMetrics)
}
```

## Cardinality Control

### Bounded Cardinality
Faro core metrics have **controlled cardinality**:
- **GVR Labels**: Bounded by configured resources (typically 10-100)
- **Status Labels**: Fixed enums (`active`, `syncing`, `failed`)
- **Event Type Labels**: Fixed enums (`ADDED`, `UPDATED`, `DELETED`)

### Cardinality Estimates
- **Small deployment** (10-20 GVRs): ~100-300 series
- **Large deployment** (50-100 GVRs): ~500-1,500 series  
- **Enterprise deployment** (100+ GVRs): ~1,000-3,000 series

### Library User Responsibility
Library users control cardinality for business metrics:
```go
// Good: Bounded labels
workload_detected_total{workload_type="batch", namespace_pattern="prod-*"}

// Bad: Unbounded labels (avoid)
workload_detected_total{namespace="prod-app-12345", pod_name="app-xyz-abc"}
```

## Troubleshooting

### Missing Core Metrics
If Faro core metrics are not appearing:
1. Verify `metrics.enabled: true` in configuration
2. Check metrics server is running: `curl http://localhost:8080/metrics`
3. Verify informers are starting: `faro_informers_total{status="active"}`

### High Cardinality
If metrics cardinality is high:
```promql
# Check series count per metric
count by (__name__) ({__name__=~"faro_.*"})

# Monitor business logic metrics separately
count by (__name__) ({__name__!~"faro_.*"})
```

### Performance Issues
If metrics impact performance:
1. **Core metrics**: Always lightweight, contact maintainers if issues
2. **Business metrics**: Review library user implementation for efficiency

## Design Principles

### 1. Core Library Focus
- **Informer Mechanisms**: Lifecycle, sync, health
- **Event Streaming**: Processing rates, latency
- **Resource Tracking**: Cache efficiency, memory usage

### 2. No Business Logic
- **No Workload Metrics**: Library users implement workload detection metrics
- **No CRD Metrics**: Library users implement CRD discovery metrics
- **No Integration Metrics**: Library users implement external system metrics

### 3. Predictable Cardinality
- **Bounded Labels**: All labels use controlled values
- **No User Content**: No namespace names, resource names in labels
- **Efficient Aggregation**: Metrics designed for low memory usage

This ensures **Faro core metrics remain focused on library mechanisms** while allowing users to implement comprehensive business logic metrics for their specific use cases.