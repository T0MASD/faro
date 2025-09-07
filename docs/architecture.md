# Faro Architecture

Kubernetes resource monitoring library with advanced filtering, dynamic workload detection, and production-ready examples.

## System Overview

**Purpose**: Go library for Kubernetes resource monitoring with specialized examples for production use cases. Core library provides foundational capabilities while examples like `workload-monitor` demonstrate advanced patterns for real-world deployments.

**Key Characteristics**:
- **Library-First Design**: Core monitoring library with production examples
- **Advanced Filtering**: Three-tier GVR filtering (allowed/denied/workload)
- **Dynamic Workload Detection**: Label-based discovery with namespace patterns
- **Namespace-Scoped Efficiency**: Per-workload informers for optimal resource usage
- **Watchability Validation**: 400+ GVRs validated for watch capability
- **Production Examples**: workload-monitor for real-world monitoring scenarios

## Core Components

### Core Library Layer
- **Configuration System**: YAML-driven configuration with flexible resource targeting
- **Discovery Engine**: Runtime enumeration of 400+ Kubernetes GVRs with watchability validation
- **Controller Architecture**: Multi-layered informer management with dynamic creation/destruction
- **Event Processing**: Work queue pattern with exponential backoff and graceful shutdown
- **Library Interface**: Event handler callbacks for custom monitoring applications

### Workload Monitor Enhancement
- **Dynamic Detection**: Label-based workload discovery across cluster namespaces
- **Three-Tier Filtering**: Allowed GVRs (cluster-wide) + Workload GVRs (per-namespace) + Denied GVRs (exclusion)
- **Namespace-Scoped Informers**: Per-workload resource monitoring for efficiency
- **Structured Logging**: JSON output with workload context and metadata
- **Production Ready**: Command-line interface optimized for deployment scenarios

### Discovery Engine
- **API Resource Discovery**: Runtime enumeration of all cluster API groups and resources
- **Watchability Validation**: Filters out non-watchable resources (componentstatuses, bindings, metrics.*)
- **Scope Detection**: Automatic determination of cluster vs namespace-scoped resources
- **CRD Monitoring**: Real-time CustomResourceDefinition detection for dynamic informer creation
- **Version Handling**: Multi-version API resource support

### Controller Architecture

**Core Library Flow:**
```
Resource Change → Informer → Work Queue → Worker → Reconcile → Event Handlers
```

**Workload Monitor Flow:**
```
Namespace Detection → Workload Extraction → Dynamic Informer Creation → Resource Events → Structured Logging
```

**Components**:
- **Controller**: Main orchestrator with work queue pattern and dynamic informer management
- **Informer Management**: Dynamic creation/destruction with namespace-scoped efficiency
- **Worker Pool**: Asynchronous event processing with rate limiting and retries
- **Event Handlers**: Callback interface for library consumers and specialized examples
- **Workload Detection**: Label-based discovery with namespace pattern matching
- **Filtering System**: Three-tier GVR filtering for optimal resource usage

### Work Queue System
- **Pattern**: Standard Kubernetes controller pattern with `workqueue.RateLimitingInterface`
- **Workers**: Configurable goroutine pool (default: 3)
- **Retry Logic**: Exponential backoff for failed event processing
- **Event Types**: ADDED, UPDATED, DELETED with proper object key extraction

## Data Structures

### Core Library Types
```go
type WorkItem struct {
    Key       string             // namespace/name or name
    GVRString string             // group/version/resource
    Configs   []NormalizedConfig // applicable filtering rules
    EventType string             // ADDED/UPDATED/DELETED
}

type MatchedEvent struct {
    EventType string                      // ADDED/UPDATED/DELETED
    Object    *unstructured.Unstructured  // Full Kubernetes object
    GVR       string                      // Group/Version/Resource identifier
    Key       string                      // namespace/name or name
    Config    NormalizedConfig            // Configuration that matched
    Timestamp time.Time                   // When processed
}

type EventHandler interface {
    OnMatched(event MatchedEvent) error
}
```

### Workload Monitor Types
```go
type WorkloadMonitor struct {
    client                   *faro.KubernetesClient
    logger                   *faro.Logger
    sharedController         *faro.Controller
    
    // Configuration
    detectionLabel           string        // Label key for workload detection
    workloadNamePattern      *regexp.Regexp // Pattern to match workload names
    namespacePattern         string        // Pattern for related namespaces
    cmdAllowedGVRs           []string      // Cluster-wide monitoring GVRs
    cmdDeniedGVRs            []string      // Explicitly excluded GVRs
    cmdWorkloadGVRs          []string      // Per-namespace monitoring GVRs
    
    // State tracking
    detectedWorkloads        map[string]bool
    monitoredNamespaces      map[string]bool
    namespaceToWorkloadID    map[string]string
    workloadIDToWorkloadName map[string]string
}

type WorkloadContext struct {
    WorkloadID   string            `json:"workload_id"`
    WorkloadName string            `json:"workload_name,omitempty"`
    Namespace    string            `json:"namespace,omitempty"`
    ResourceType string            `json:"resource_type"`
    ResourceName string            `json:"resource_name"`
    Action       string            `json:"action"`
    UID          string            `json:"uid,omitempty"`
    Labels       map[string]string `json:"labels,omitempty"`
}
```

### Configuration Approaches

**Core Library Configuration (YAML):**
```yaml
namespaces:
  - name_pattern: "prod-.*"
    resources:
      "v1/pods":
        name_pattern: "web-.*"
        label_selector: "app=nginx"
      "v1/configmaps":
        name_pattern: ".*"
        label_pattern: "app=^nginx-.*$"
```

**Workload Monitor Configuration (Command-Line):**
```bash
./workload-monitor \
  -label "api.openshift.com/name" \
  -pattern "toda-.*" \
  -namespace-pattern "ocm-staging-(.+)" \
  -allowedgvrs "v1/namespaces" \
  -workloadgvrs "v1/pods,v1/configmaps,v1/services,v1/secrets"
```

**Three-Tier Filtering System:**
- **allowedgvrs**: Cluster-wide monitoring (minimal set for workload detection)
- **deniedgvrs**: Explicitly excluded GVRs (noise reduction)
- **workloadgvrs**: Per-namespace monitoring (created dynamically per detected workload)

## Runtime Behavior

### Core Library Startup
1. **API Discovery**: Enumerate all cluster API resources with watchability validation
2. **Configuration Normalization**: Convert YAML to internal format
3. **Informer Creation**: Start informers for matching discovered resources
4. **CRD Watcher**: Monitor for new CustomResourceDefinition additions
5. **Worker Pool**: Start event processing workers

### Workload Monitor Startup
1. **Command-Line Parsing**: Parse filtering flags and workload detection parameters
2. **GVR Discovery**: Enumerate 400+ cluster GVRs with watchability validation
3. **Filtering Application**: Apply three-tier filtering (allowed/denied/workload GVRs)
4. **Initial Informers**: Create cluster-wide informers for allowed GVRs (typically just v1/namespaces)
5. **Workload Detection**: Monitor for namespaces matching label and pattern criteria
6. **Dynamic Expansion**: Create namespace-scoped informers for workload GVRs per detected workload

### Dynamic Adaptation
- **Workload Detection**: Label-based namespace discovery triggers informer creation
- **Namespace-Scoped Informers**: Created per workload for optimal efficiency
- **Resource Filtering**: Client-side workload context applied to all events
- **Graceful Cleanup**: Informer lifecycle managed automatically

### Event Processing

**Core Library Flow:**
1. **Event Detection**: Informer detects resource change
2. **Key Extraction**: Generate namespace/name key from object metadata
3. **Work Queuing**: Create `WorkItem` and enqueue for processing
4. **Worker Processing**: Pull from queue, validate against configuration
5. **Label Filtering**: Apply both Kubernetes and regex label filters
6. **Event Handler Callbacks**: Execute registered handlers with `MatchedEvent`

**Workload Monitor Flow:**
1. **Namespace Detection**: v1/namespaces events trigger workload detection logic
2. **Workload Extraction**: Apply label and namespace pattern matching
3. **Dynamic Informer Creation**: Create namespace-scoped informers for workload GVRs
4. **Resource Event Processing**: Apply client-side workload context filtering
5. **Structured Logging**: Output JSON with workload metadata and context
6. **State Management**: Track detected workloads and monitored namespaces

### Advanced Filtering Architecture

#### Three-Tier GVR Filtering (Workload Monitor)

Optimal resource efficiency through strategic GVR categorization:

```go
// Filtering logic in workload-monitor
func (w *WorkloadMonitor) filterGVRs(discoveredGVRs map[string]*faro.ResourceInfo) map[string]*faro.ResourceInfo {
    // 1. Start with allowed GVRs (cluster-wide monitoring)
    allowedGVRs := parseGVRList(w.cmdAllowedGVRs)
    
    // 2. Add workload GVRs to denied list (will be monitored per-namespace)
    workloadGVRs := parseGVRList(w.cmdWorkloadGVRs)
    deniedGVRs := append(parseGVRList(w.cmdDeniedGVRs), workloadGVRs...)
    
    // 3. Apply filtering logic
    filtered := make(map[string]*faro.ResourceInfo)
    for gvr, info := range discoveredGVRs {
        if contains(allowedGVRs, gvr) {
            filtered[gvr] = info  // Cluster-wide monitoring
        } else if !contains(deniedGVRs, gvr) {
            // Default behavior for unspecified GVRs
        }
    }
    return filtered
}
```

**Performance Benefits:**
- **Allowed GVRs**: Minimal cluster-wide monitoring (typically just v1/namespaces)
- **Workload GVRs**: Namespace-scoped informers created per detected workload
- **Denied GVRs**: Explicit exclusion of high-volume, low-value resources

#### Watchability Validation

```go
// Filters out non-watchable resources during API discovery
func isResourceWatchable(resource metav1.APIResource) bool {
    // Check for watch verb
    for _, verb := range resource.Verbs {
        if verb == "watch" {
            // Additional filtering for known problematic resources
            problematicResources := []string{
                "componentstatuses", "bindings", "metrics.k8s.io",
            }
            return !contains(problematicResources, resource.Name)
        }
    }
    return false
}
```

#### Label-Based Workload Detection

```go
// Workload detection logic
func (w *WorkloadMonitor) handleNamespaceDetection(event faro.MatchedEvent) error {
    labels := event.Object.GetLabels()
    if labelValue, exists := labels[w.detectionLabel]; exists {
        if w.workloadNamePattern.MatchString(labelValue) {
            workloadID := extractWorkloadID(event.Object.GetName(), w.namespacePattern)
            w.addWorkloadToClientFiltering(workloadID, labelValue)
            return w.createNamespaceScopedInformers(workloadID)
        }
    }
    return nil
}
```

#### Unified Processing Flow

**Core Library:**
```
Resource Event → Informer → Work Queue → Worker → Filter Chain → Event Handlers
                     ↑                              ↓
              [Server-side]                  [Client-side]
              label_selector                 label_pattern
```

**Workload Monitor:**
```
Namespace Event → Workload Detection → Dynamic Informer Creation → Resource Events → Workload Context → Structured Logging
       ↑                    ↓                           ↑                        ↓
[Label Detection]    [Namespace-Scoped]           [Per-Workload]         [JSON Output]
```

**Processing Order (Workload Monitor):**
1. **GVR Filtering**: Three-tier filtering (allowed/denied/workload GVRs)
2. **Workload Detection**: Label-based namespace discovery
3. **Dynamic Informers**: Namespace-scoped informer creation per workload
4. **Resource Events**: Client-side workload context application
5. **Structured Output**: JSON logging with workload metadata

#### Use Case Optimization

**Production Workload Monitoring** (workload-monitor):
```bash
# Optimal efficiency: minimal cluster-wide + per-workload namespace-scoped
./workload-monitor \
  -allowedgvrs "v1/namespaces" \
  -workloadgvrs "v1/pods,v1/configmaps,v1/services" \
  -deniedgvrs "coordination.k8s.io/v1/leases,events.k8s.io/v1/events"
```

**Core Library - High-volume, simple filtering**:
```yaml
# Use label_selector for performance
resources:
  - gvr: "v1/pods"
    label_selector: "app=nginx,environment=production"
```

**Core Library - Complex pattern matching**:
```yaml
# Use label_pattern for flexibility
resources:
  - gvr: "hypershift.openshift.io/v1beta1/hostedclusters"
    label_pattern: "kubernetes.io/metadata.name=^ocm-staging-[a-z0-9]{32}-cs-ci-.*$"
```

**Performance Comparison:**
- **Workload Monitor**: Scales with workload count (5 workloads = 5×3 = 15 namespace-scoped informers)
- **Traditional**: Scales with cluster size (1 cluster-wide informer processing all events)
- **Efficiency Gain**: 95%+ reduction in processed events for workload-specific monitoring

## Library Interface

### Event Handler Registration (Core Library)
```go
// Register handlers for matched events
controller.AddEventHandler(&MyHandler{})

// Handler receives filtered events
type MyHandler struct{}
func (h *MyHandler) OnMatched(event MatchedEvent) error {
    // Process matched Kubernetes resource event
    return nil
}
```

### Workload Monitor Implementation
```go
// Workload monitor implements EventHandler interface
type WorkloadMonitor struct {
    // ... fields ...
}

func (w *WorkloadMonitor) OnMatched(event faro.MatchedEvent) error {
    // Handle namespace detection for workload discovery
    if event.GVR == "v1/namespaces" && event.EventType == "ADDED" {
        return w.handleNamespaceDetection(event)
    }
    
    // Handle all resource events with workload context
    return w.handleResourceEventWithClientFiltering(event)
}
```

### Usage Patterns
- **Core Library**: Custom event handlers for specialized monitoring
- **Workload Monitor**: Production-ready example with structured logging
- **CLI Tool**: Basic monitoring with file output
- **Configuration**: YAML (library) vs command-line (workload-monitor)

**See**: [Library Usage Guide](library-usage.md) for comprehensive examples and integration patterns.

## Key Design Decisions

### Library-First Architecture
- **Core Library**: Foundational Kubernetes monitoring capabilities
- **Production Examples**: workload-monitor demonstrates real-world patterns
- **Extensibility**: Event handler interface enables custom monitoring applications
- **Reusability**: Same core library powers CLI tool and specialized examples

### Three-Tier Filtering Strategy
- **Allowed GVRs**: Minimal cluster-wide monitoring for workload detection
- **Workload GVRs**: Namespace-scoped informers created per detected workload
- **Denied GVRs**: Explicit exclusion of high-volume, low-value resources
- **Efficiency**: Resource usage scales with workloads, not cluster size

### Dynamic Informer Management
- **Namespace-Scoped Creation**: Informers created per workload for optimal efficiency
- **Watchability Validation**: 400+ GVRs validated for watch capability during discovery
- **Lifecycle Management**: Automatic creation/destruction based on workload detection
- **Deduplication**: One informer per GVR+namespace combination

### Workload Detection Pattern
- **Label-Based Discovery**: Configurable label key and pattern matching
- **Namespace Patterns**: Regex-based namespace relationship detection
- **State Tracking**: Persistent workload and namespace mapping
- **Client-Side Filtering**: Workload context applied to all resource events

### Memory and Performance Management
- **Context Cancellation**: Individual cancel contexts for each informer
- **Graceful Shutdown**: Wait groups ensure complete cleanup
- **Resource Tracking**: Concurrent-safe maps for informer lifecycle management
- **Structured Logging**: JSON output with workload metadata for analysis

## Dependencies

### Kubernetes Client Libraries
- `k8s.io/client-go`: Dynamic client, informers, work queues
- `k8s.io/apimachinery`: Schema definitions, label selectors
- `k8s.io/apiextensions-apiserver`: CRD type definitions

### Core Go Libraries
- `context`: Cancellation and timeout management
- `sync`: Concurrent data structure management
- `regexp`: Pattern matching for resource filtering

## File Organization

```
pkg/
├── client.go     # Kubernetes client initialization
├── config.go     # Configuration parsing and normalization  
├── controller.go # Controller, informer management, event handlers
└── logger.go     # Callback-based logging system
main.go           # Basic CLI entry point
examples/
├── workload-monitor.go      # Production workload monitoring (primary example)
├── library-usage.go         # Basic library usage patterns
└── worker-dispatcher.go     # Advanced event processing patterns
e2e/
├── test*.sh      # Comprehensive test suite (CLI + library + workload-monitor)
├── test8.go      # Library-based test implementation
└── test10.go     # Dynamic namespace discovery test
docs/
├── architecture.md          # System design and patterns
├── library-usage.md         # Library integration guide
├── audit-requirements.md    # Production monitoring validation
└── components/              # Component-specific documentation
```

## Concurrency Model

- **Event Handlers**: Lightweight, non-blocking key extraction
- **Worker Pool**: Configurable goroutine count for event processing
- **Informer Isolation**: Individual context cancellation per informer
- **Thread Safety**: Sync.Map for concurrent access to informer metadata

## Error Handling

### Core Library
- **Work Queue Retries**: Automatic retry with exponential backoff
- **Discovery Failures**: Graceful degradation with partial functionality
- **Invalid Patterns**: Configuration validation with clear error reporting
- **Resource Conflicts**: Deduplication logic prevents informer conflicts

### Workload Monitor Enhancements
- **Watchability Validation**: Filters out non-watchable resources during discovery
- **Dynamic Informer Failures**: Graceful handling of namespace-scoped informer creation errors
- **Workload Detection Errors**: Robust pattern matching with fallback behavior
- **State Consistency**: Atomic operations for workload and namespace tracking
- **Structured Error Logging**: JSON error output with workload context