# Faro Architecture

Kubernetes resource monitoring Go library with informer-based event processing.

## System Overview

**Purpose**: Go library for monitoring Kubernetes resources using informers with configurable filtering and event callbacks.

**Key Characteristics**:
- **Library-First Design**: Core monitoring library with callback-based event handling
- **Server-Side Filtering**: Kubernetes API server-side filtering for namespaces, labels, and resource names
- **Multi-Layered Informers**: Dynamic informer creation with CRD discovery
- **Work Queue Pattern**: Asynchronous event processing with rate limiting
- **JSON Export**: Optional structured event export to files

## Core Components

### Configuration System (`pkg/config.go`)
- **YAML Configuration**: Supports both namespace-centric and resource-centric formats
- **Server-Side Filtering**: Label selectors, namespace names, and exact resource names
- **Normalization**: Converts both config formats to unified internal representation
- **Validation**: Config validation with sensible defaults

### Controller (`pkg/controller.go`)
- **Multi-Layered Informers**: Dynamic informer creation for configured resources
- **Work Queue**: Asynchronous event processing with rate limiting
- **CRD Discovery**: Real-time CustomResourceDefinition monitoring
- **Event Callbacks**: Handler interface for library consumers
- **Graceful Shutdown**: Proper informer lifecycle management

### Kubernetes Client (`pkg/client.go`)
- **Client Wrapper**: Kubernetes clientset and dynamic client management
- **Configuration**: Supports both in-cluster and kubeconfig-based auth

### Logger (`pkg/logger.go`)
- **Multi-Handler Logging**: Console, file, and JSON export handlers
- **Callback Interface**: Pluggable log handlers
- **Structured Events**: JSON event export with timestamps and metadata

### Controller Architecture

**Event Processing Flow:**
```
Resource Event → Informer → Work Queue → Worker → Event Handler Callbacks
```

**Components**:
- **Controller**: Main orchestrator with work queue pattern and informer management
- **Informer Management**: Dynamic creation/destruction with server-side filtering
- **Worker Pool**: Asynchronous event processing with rate limiting and retries
- **Event Handlers**: Callback interface for library consumers
- **CRD Watcher**: Dynamic informer creation for CustomResourceDefinitions

### Work Queue System
- **Approach**: Standard Kubernetes controller approach with `workqueue.RateLimitingInterface`
- **Workers**: Configurable goroutine pool (default: 3)
- **Retry Logic**: Exponential backoff for failed event processing
- **Event Types**: ADDED, UPDATED, DELETED with proper object key extraction

## Data Structures

### Core Types
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

type JSONEvent struct {
    Timestamp string            `json:"timestamp"`
    EventType string            `json:"eventType"`
    GVR       string            `json:"gvr"`
    Namespace string            `json:"namespace,omitempty"`
    Name      string            `json:"name"`
    UID       string            `json:"uid,omitempty"`
    Labels    map[string]string `json:"labels,omitempty"`
}
```

### Configuration Formats

**Namespace-Centric Configuration:**
```yaml
output_dir: "./logs"
log_level: "info"
auto_shutdown_sec: 120
json_export: true

namespaces:
  - name_selector: "prod-.*"
    resources:
      "v1/pods":
        label_selector: "app=nginx"
      "v1/configmaps":
        name_selector: "config-.*"
```

**Resource-Centric Configuration:**
```yaml
output_dir: "./logs"
log_level: "info"
json_export: true

resources:
  - gvr: "v1/pods"
    scope: "Namespaced"
    namespace_names: ["default", "kube-system"]
    label_selector: "app=nginx"
  - gvr: "v1/namespaces"
    scope: "Cluster"
    name_selector: "prod-.*"
```

## Runtime Behavior

### Startup Sequence
1. **Configuration Loading**: Parse YAML config and normalize to internal format
2. **API Discovery**: Enumerate cluster API resources (395 resources from 34 API groups)
3. **Watchability Validation**: Filter resources by 'watch' verb support, exclude problematic resources
4. **Informer Creation**: Create informers for configured resources with server-side filtering
5. **CRD Watcher**: Start monitoring for CustomResourceDefinition additions
6. **Worker Pool**: Start event processing workers (default: 3 goroutines)
7. **Readiness Callback**: Signal "Multi-layered informer architecture started successfully"

### Server-Side Filtering
- **Namespace Filtering**: Applied at informer creation for namespace-scoped resources
- **Label Selectors**: Kubernetes-native label filtering on API server  
- **Field Selectors**: Exact resource name matching (metadata.name=exact-value)
- **Faro Core Processing**: All events matching server-side filters are processed and forwarded to application handlers (verified by tests)

**Why Server-Side Filtering:**
- **API Server Efficiency**: The Kubernetes API server receives requests and performs filtering based on selectors, only retrieving resources that match criteria
- **etcd Optimization**: The etcd database backing the API server is designed for efficient key lookups and watches, handling filtered requests without transmitting entire datasets
- **Network Efficiency**: Only matching resources are transferred from API server to client, reducing bandwidth usage
- **Memory Efficiency**: Client applications receive only relevant data, minimizing memory footprint
- **CPU Efficiency**: Faro core requires no client-side pattern matching or filtering logic (applications using Faro may implement additional filtering)

### Event Processing

**Processing Flow:**
1. **Event Detection**: Informer detects resource change (ADDED/UPDATED/DELETED)
2. **Key Extraction**: Generate namespace/name key from object metadata
3. **Work Queuing**: Create `WorkItem` and enqueue for asynchronous processing
4. **Worker Processing**: Pull from queue and validate against configuration
5. **Event Handler Callbacks**: Execute registered handlers with `MatchedEvent`
6. **JSON Export**: Optional structured event export to timestamped files

**CRD Discovery:**
1. **CRD Monitoring**: Watch for CustomResourceDefinition additions
2. **Dynamic Informer Creation**: Automatically create informers for matching CRDs
3. **Lifecycle Management**: Handle CRD deletion and informer cleanup

### Filtering Architecture

#### Server-Side Filtering

Faro leverages Kubernetes API server-side filtering for optimal efficiency and performance:

**Implementation Details:**
- **Namespace Filtering**: Informers created with namespace restrictions for namespace-scoped resources
- **Label Selectors**: Standard Kubernetes label selectors applied at the API server via `ListOptions.LabelSelector`
- **Field Selectors**: Exact resource name matching using `metadata.name=exact-value` via `ListOptions.FieldSelector`
- **Faro Core Processing**: All events matching server-side filters are processed and forwarded to application event handlers (verified by integration tests)

**Kubernetes API Server Processing:**
1. **Request Reception**: API server receives LIST/WATCH requests with selector parameters
2. **etcd Query Optimization**: Server constructs efficient etcd queries based on selectors
3. **Filtered Retrieval**: Only resources matching criteria are retrieved from etcd storage
4. **Selective Transmission**: API server transmits only matching resources to client
5. **Watch Efficiency**: Subsequent watch events are pre-filtered before transmission

**etcd Database Benefits:**
- **Key-Value Optimization**: etcd's design enables efficient key lookups and range queries
- **Watch Streams**: Native support for filtered watch streams reduces unnecessary data transfer
- **Index Utilization**: Label and field selectors leverage etcd's indexing capabilities
- **Memory Conservation**: Server-side filtering prevents loading entire resource sets into memory

#### Processing Flow

```
Resource Event → Informer → Work Queue → Worker → Event Handler Callbacks → Application Filtering
                     ↑                                                              ↑
              [Server-side filtering]                                    [Application-specific filtering]
              (namespaces, labels, names)                                (custom logic, complex selectors)
```

**Processing Responsibilities:**
- **Faro Core**: Handles server-side filtering via Kubernetes API selectors and forwards all matching events
- **Applications**: Implement additional client-side filtering logic in event handlers as needed for complex selectors, business logic, or advanced matching criteria
- **Worker Dispatchers**: Can be set up to process matched events and take further actions on resource activity:
  - Create, update, or delete related resources
  - Send notifications or alerts  
  - Update external systems or databases
  - Trigger workflows or automation
  - Apply business logic based on resource state

#### Configuration Examples

**Simple Resource Monitoring:**
```yaml
output_dir: "./logs"
log_level: "info"
json_export: true

namespaces:
  - name_selector: "default"
    resources:
      "v1/pods":
        label_selector: "app=nginx"
```

**Multi-Namespace Monitoring:**
```yaml
resources:
  - gvr: "v1/configmaps"
    scope: "Namespaced"
    namespace_names: ["kube-system", "default"]
    name_selector: "config-.*"
```

## Library Interface

### Event Handler Registration
```go
// Register handlers for matched events
controller.AddEventHandler(&MyHandler{})

// Handler receives filtered events
type MyHandler struct{}
func (h *MyHandler) OnMatched(event MatchedEvent) error {
    // Process matched Kubernetes resource event
    fmt.Printf("Event: %s %s/%s\n", event.EventType, event.GVR, event.Key)
    return nil
}
```

### Basic Usage
```go
// Load configuration
config, err := faro.LoadConfig()
if err != nil {
    log.Fatal(err)
}

// Create Kubernetes client
client, err := faro.NewKubernetesClient()
if err != nil {
    log.Fatal(err)
}

// Create logger
logger, err := faro.NewLogger(config)
if err != nil {
    log.Fatal(err)
}

// Create controller
controller := faro.NewController(client, logger, config)

// Register event handler
controller.AddEventHandler(&MyHandler{})

// Set readiness callback
controller.SetReadyCallback(func() {
    fmt.Println("Faro is ready!")
})

// Start monitoring
if err := controller.Start(); err != nil {
    log.Fatal(err)
}

// Wait for shutdown signal
// ... 

// Stop gracefully
controller.Stop()
```

## Key Design Decisions

### Library-First Architecture
- **Core Library**: Foundational Kubernetes monitoring capabilities
- **Event Handler Interface**: Enables custom monitoring applications
- **Extensibility**: Same core library can power different use cases
- **Reusability**: Clean separation between core functionality and specific implementations

### Server-Side Filtering Strategy
- **Efficiency**: Leverage Kubernetes API server filtering capabilities
- **Performance**: Reduce network traffic and client-side processing
- **Simplicity**: No complex client-side filtering logic
- **Reliability**: Use proven Kubernetes filtering mechanisms

### Dynamic Informer Management
- **CRD Support**: Automatic informer creation for CustomResourceDefinitions
- **Lifecycle Management**: Proper creation/destruction of informers
- **Resource Efficiency**: Only create informers for configured resources
- **Graceful Shutdown**: Clean termination of all informers and workers

### Work Queue Pattern
- **Asynchronous Processing**: Decouple event detection from processing
- **Rate Limiting**: Built-in exponential backoff for failed events
- **Reliability**: Standard Kubernetes controller pattern
- **Scalability**: Configurable worker pool for concurrent processing

## Dependencies

### Kubernetes Client Libraries
- `k8s.io/client-go`: Dynamic client, informers, work queues
- `k8s.io/apimachinery`: Schema definitions, label selectors
- `k8s.io/apiextensions-apiserver`: CRD type definitions

### Core Go Libraries
- `context`: Cancellation and timeout management
- `sync`: Concurrent data structure management
- `encoding/json`: JSON event export
- `gopkg.in/yaml.v2`: YAML configuration parsing

## File Organization

```
pkg/
├── client.go     # Kubernetes client initialization
├── config.go     # Configuration parsing and normalization  
├── controller.go # Controller, informer management, event handlers
└── logger.go     # Callback-based logging system
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
- **Graceful Shutdown**: Proper cleanup of all resources and goroutines