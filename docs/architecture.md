# Faro Architecture

## Philosophy: Mechanisms vs. Policies

Faro follows a **clean architecture principle**: the library provides **mechanisms**, and users implement **policies**.

### Core Principle
- **Library Role**: Provide reliable, efficient tools for Kubernetes resource monitoring
- **User Role**: Implement business logic, complex filtering, and application-specific workflows

### Architectural Boundaries

```
┌─────────────────────────────────────────────────────────────┐
│                    LIBRARY USERS (Policies)                 │
├─────────────────────────────────────────────────────────────┤
│ • CRD Discovery & Management                                │
│ • Event-driven GVR Discovery                               │
│ • Workload Detection & Annotation                          │
│ • Complex Configuration Interpretation                     │
│ • Business Logic & Workflows                               │
│ • External System Integration                              │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                   FARO CORE (Mechanisms)                    │
├─────────────────────────────────────────────────────────────┤
│ • Informer Management (Create, Start, Stop)                │
│ • Event Streaming (Work Queues, Handlers)                  │
│ • Server-side Filtering (Labels, Names, Namespaces)        │
│ • JSON Export (Structured Output)                          │
│ • Lifecycle Management (Startup, Readiness, Shutdown)      │
│ • Simple Configuration (Basic YAML Parsing)                │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                     KUBERNETES API                          │
└─────────────────────────────────────────────────────────────┘
```

## Core Architecture

### 1. Controller (Informer Management)
**Purpose**: Manage Kubernetes informers with clean lifecycle

**Responsibilities**:
- Create namespace-specific informers for efficient filtering
- Start/stop informers with proper synchronization
- Handle informer lifecycle events (sync, ready, shutdown)
- Provide unified interface for informer management

**Key Features**:
- **Single Informer Creation Path**: All informers use `createNamespaceSpecificInformer`
- **Consistent Lister Keys**: `GVRString@namespace` format for all listers
- **Pure Event-driven**: No timeouts, callbacks for sync detection
- **Graceful Shutdown**: Context-based cancellation with proper cleanup

### 2. Configuration (Simple Parsing)
**Purpose**: Parse basic YAML configuration without complex interpretation

**Supported Formats**:
```yaml
# Namespace-centric format
namespaces:
  - name_pattern: "production"
    resources:
      "v1/configmaps":
        label_selector: "app=nginx"

# Resource-centric format  
resources:
  - gvr: "v1/configmaps"
    namespace_names: ["production"]
    label_selector: "app=nginx"
```

**Key Principles**:
- **Simple Conversion**: Basic normalization without business logic
- **No Complex Interpretation**: No regex parsing, pattern matching, or defaults
- **Error Propagation**: Invalid configurations cause errors, not fallbacks

### 3. Event Processing (Work Queue Pattern)
**Purpose**: Reliable event delivery with standard Kubernetes patterns

**Flow**:
```
Kubernetes Informer → Work Queue → Worker Goroutines → Event Handlers
```

**Features**:
- **Rate Limiting**: Exponential backoff for failed events
- **Deduplication**: Consistent resource keys prevent duplicate processing
- **Error Handling**: Proper error propagation without fallbacks
- **Thread Safety**: Concurrent processing with proper synchronization

### 4. JSON Export (Structured Output)
**Purpose**: Provide structured event data for integration

**Output Format**:
```json
{
  "timestamp": "2025-01-18T10:30:45Z",
  "eventType": "ADDED",
  "gvr": "v1/configmaps", 
  "namespace": "default",
  "name": "app-config",
  "uid": "12345678-1234-1234-1234-123456789012",
  "labels": {"app": "web"},
  "annotations": {"faro.workload.id": "test123"}
}
```

**Key Features**:
- **No Special Field Processing**: Library users add custom fields via middleware
- **Consistent Format**: Standard fields for all resource types
- **Extensible**: Event handlers can modify events before export

## Removed Business Logic

The following functionality was **removed from Faro core** and moved to **library user responsibility**:

### 1. CRD Discovery & Management
**Previously**: Faro automatically watched CRDs and started informers
**Now**: Library users implement CRD watching if needed

```go
// Library user implements CRD discovery
type CRDWatcher struct {
    controller *faro.Controller
}

func (c *CRDWatcher) OnMatched(event faro.MatchedEvent) error {
    if event.GVR == "apiextensions.k8s.io/v1/customresourcedefinitions" {
        return c.handleCRDEvent(event)
    }
    return nil
}
```

### 2. Event-driven GVR Discovery  
**Previously**: Faro processed `v1/events` to discover new GVRs
**Now**: Library users implement dynamic discovery if needed

```go
// Library user implements dynamic discovery
type EventProcessor struct {
    controller *faro.Controller
}

func (e *EventProcessor) OnMatched(event faro.MatchedEvent) error {
    if event.GVR == "v1/events" {
        return e.processEventForDiscovery(event)
    }
    return nil
}
```

### 3. Workload Annotation Processing
**Previously**: Faro automatically extracted and processed workload annotations
**Now**: Library users implement annotation processing via middleware

```go
// Library user implements workload processing
type WorkloadMiddleware struct{}

func (w *WorkloadMiddleware) OnMatched(event faro.MatchedEvent) error {
    // Extract workload ID from namespace
    workloadID := extractWorkloadID(event.Object.GetNamespace())
    
    // Add workload annotations
    event.Object.SetAnnotations(map[string]string{
        "faro.workload.id": workloadID,
        "faro.workload.name": "faro",
    })
    
    return nil
}
```

## Design Principles

### 1. No Fallbacks or Defaults
- **Strict Error Propagation**: Invalid configurations cause errors
- **No Hidden Behavior**: All functionality is explicit and visible
- **Fail Fast**: Problems are surfaced immediately, not masked

### 2. Single Responsibility
- **Informer Management**: Core library manages informer lifecycle only
- **Business Logic**: Library users implement all application-specific logic
- **Clean Separation**: Clear boundaries between mechanisms and policies

### 3. Pure Event-driven Design
- **No Timeouts**: All operations use callbacks and events
- **Sync Detection**: `sync.Once` based callbacks for informer readiness
- **Context-based Cancellation**: Graceful shutdown without arbitrary timeouts

### 4. Consistent Patterns
- **Single Informer Path**: All informers created via `createNamespaceSpecificInformer`
- **Unified Lister Keys**: `GVRString@namespace` format for all resources
- **Standard Work Queues**: Kubernetes controller patterns throughout

## Integration Examples

### Basic Integration
```go
// Simple library usage
config, _ := faro.LoadConfig()
client, _ := faro.NewKubernetesClient()
logger, _ := faro.NewLogger(config)
controller := faro.NewController(client, logger, config)

// Register business logic
controller.AddEventHandler(&MyBusinessLogic{})

controller.Start()
```

### Advanced Integration (Workload Monitor)
```go
// Complex business logic implementation
type WorkloadMonitor struct {
    controller *faro.Controller
    // Business logic state
}

func (w *WorkloadMonitor) OnMatched(event faro.MatchedEvent) error {
    // Implement workload detection
    // Process events for dynamic discovery
    // Add workload annotations
    // Integrate with external systems
    return w.processWorkloadEvent(event)
}
```

## Testing Architecture

### Unit Tests
- **Core Library Only**: Test informer management, configuration parsing
- **No Kubernetes Required**: Mock interfaces and dependency injection
- **Fast Execution**: Focused on library mechanisms

### Integration Tests  
- **Library User Implementations**: Test business logic implementations
- **Real Kubernetes**: Validate against actual cluster
- **Dynamic Discovery**: Test CRD and event-driven discovery

### E2E Tests
- **End-to-End Scenarios**: Complete workflows with real configurations
- **Multiple Formats**: Test both namespace and resource configuration formats
- **Comprehensive Coverage**: All core library functionality

## Performance Characteristics

### Efficiency
- **Server-side Filtering**: Minimal network traffic via Kubernetes API
- **Namespace-scoped Informers**: Efficient per-namespace filtering
- **Single Informer per GVR+Namespace**: No duplicate informers

### Scalability
- **Resource Usage**: Scales with configured resources, not cluster size
- **Memory Efficiency**: Minimal caching, efficient data structures
- **CPU Usage**: Event-driven processing, no polling or timeouts

### Reliability
- **Error Propagation**: Clear error handling without hidden failures
- **Graceful Shutdown**: Proper resource cleanup on termination
- **Thread Safety**: Concurrent operations with proper synchronization

This architecture ensures **Faro remains a clean, focused library** that provides reliable mechanisms while allowing users to implement their specific business logic and policies.