# Faro Architecture

Kubernetes resource observation tool with dynamic discovery and configuration-driven informer management.

## System Overview

**Purpose**: Monitor Kubernetes resource lifecycle events (ADDED/UPDATED/DELETED) across namespaced and cluster-scoped resources using dynamic informer creation.

**Key Characteristics**:
- Configuration-driven informer creation
- Real-time API discovery
- Work queue-based event processing
- Dual configuration format support

## Core Components

### Configuration Layer
- **Dual format support**: Namespace-centric and resource-centric YAML configurations
- **Normalization**: Both formats converted to unified `NormalizedConfig` internal structure
- **Label filtering**: Server-side Kubernetes label selector support
- **Pattern matching**: Regex-based resource and namespace name filtering

### Discovery Engine
- **API Resource Discovery**: Runtime enumeration of all cluster API groups and resources
- **Scope Detection**: Automatic determination of cluster vs namespace-scoped resources
- **CRD Monitoring**: Real-time CustomResourceDefinition detection for dynamic informer creation
- **Version Handling**: Multi-version API resource support

### Controller Architecture
```
Event Flow: Resource Change → Informer → Work Queue → Worker → Reconcile → Log
```

**Components**:
- **Controller**: Main orchestrator with work queue pattern
- **Informer Management**: Dynamic creation/destruction of resource informers
- **Worker Pool**: Asynchronous event processing with rate limiting and retries
- **Lister Management**: Cached object retrieval for DELETED event validation

### Work Queue System
- **Pattern**: Standard Kubernetes controller pattern with `workqueue.RateLimitingInterface`
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

type NormalizedConfig struct {
    GVR               string          // target resource
    ResourceDetails   ResourceDetails // name patterns, label selectors
    NamespacePatterns []string        // namespace filtering rules
    LabelSelector     string          // Kubernetes label selector
}

type ResourceInfo struct {
    Group      string
    Version    string
    Resource   string
    Kind       string
    Namespaced bool
}
```

### Configuration Formats

**Namespace-Centric**:
```yaml
namespaces:
  - name_pattern: "prod-.*"
    resources:
      "v1/pods":
        name_pattern: "web-.*"
        label_selector: "app=nginx"
```

**Resource-Centric**:
```yaml
resources:
  - gvr: "v1/pods"
    scope: "Namespaced"
    namespace_patterns: ["prod-.*"]
    name_pattern: "web-.*"
    label_selector: "app=nginx"
```

## Runtime Behavior

### Startup Sequence
1. **API Discovery**: Enumerate all cluster API resources
2. **Configuration Normalization**: Convert YAML to internal format
3. **Informer Creation**: Start informers for matching discovered resources
4. **CRD Watcher**: Monitor for new CustomResourceDefinition additions
5. **Worker Pool**: Start event processing workers

### Dynamic Adaptation
- **New CRDs**: Automatically evaluated against configuration patterns
- **Matching CRDs**: Dynamic informer creation without restart
- **CRD Deletion**: Graceful informer shutdown and cleanup
- **Resource Filtering**: Combined regex patterns and label selectors

### Event Processing
1. **Event Detection**: Informer detects resource change
2. **Key Extraction**: Generate namespace/name key from object metadata
3. **Work Queuing**: Create `WorkItem` and enqueue for processing
4. **Worker Processing**: Pull from queue, validate against configuration
5. **Business Logic**: Execute filtering, logging, and state tracking

## Key Design Decisions

### Informer Deduplication
- **Strategy**: One informer per GVR regardless of multiple configuration rules
- **Key Management**: Consistent GVR string format across all tracking maps
- **Lifecycle**: Shared informer with multiple configuration pattern evaluation

### Event Handler Simplicity
- **Principle**: Event handlers only extract keys and enqueue work items
- **Processing**: All business logic in dedicated `reconcile()` function
- **Benefits**: Non-blocking event detection, unified error handling

### Configuration Architecture
- **Dual Support**: Both legacy and modern configuration formats supported
- **Normalization**: Single internal processing path regardless of input format
- **Validation**: Server-side label selector application for efficiency

### Memory Management
- **Context Cancellation**: Individual cancel contexts for each informer
- **Graceful Shutdown**: Wait groups ensure complete cleanup
- **Resource Tracking**: Sync.Map usage for concurrent informer lifecycle management

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
├── controller.go # Main controller and informer management
├── logger.go     # Asynchronous logging system
└── main.go       # Entry point and lifecycle management
```

## Concurrency Model

- **Event Handlers**: Lightweight, non-blocking key extraction
- **Worker Pool**: Configurable goroutine count for event processing
- **Informer Isolation**: Individual context cancellation per informer
- **Thread Safety**: Sync.Map for concurrent access to informer metadata

## Error Handling

- **Work Queue Retries**: Automatic retry with exponential backoff
- **Discovery Failures**: Graceful degradation with partial functionality
- **Invalid Patterns**: Configuration validation with clear error reporting
- **Resource Conflicts**: Deduplication logic prevents informer conflicts