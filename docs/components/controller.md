# Controller Component

The Controller is the core component of Faro that manages Kubernetes resource monitoring through informers and event processing.

## Overview

The Controller implements a multi-layered informer architecture that:
- Discovers available Kubernetes API resources dynamically
- Creates informers for configured resources with server-side filtering
- Processes resource events through a unified pipeline
- Provides readiness callbacks for initialization synchronization
- Handles graceful shutdown with timeout protection

## Architecture

### Core Structure

```go
type Controller struct {
    client     *KubernetesClient
    config     *Config
    logger     Logger
    ctx        context.Context
    cancel     context.CancelFunc
    wg         sync.WaitGroup
    workQueue  workqueue.RateLimitingInterface
    cancellers sync.Map
    onReady    func()
    readyMu    sync.RWMutex
    isReady    bool
}
```

### Key Components

1. **Dynamic Client Integration**: Uses Kubernetes dynamic client for multi-resource support
2. **Discovery Client**: Automatically discovers available API resources (395+ resources across 34+ API groups)
3. **Work Queue**: Rate-limited work queue for asynchronous event processing
4. **Informer Management**: Unified informer creation and lifecycle management
5. **Readiness System**: Callback-based readiness notification mechanism

## Server-Side Filtering

The Controller implements comprehensive server-side filtering at the Kubernetes API level:

### FieldSelector Implementation
```go
// Applied for name pattern filtering
fieldSelector = fmt.Sprintf("metadata.name=%s", nConfig.NamePattern)
options.FieldSelector = fieldSelector
```

### LabelSelector Implementation
```go
// Applied for label-based filtering
options.LabelSelector = labelSelector
```

### Benefits
- **API Server Efficiency**: Reduces network traffic by filtering at source
- **etcd Performance**: Minimizes database queries through server-side selection
- **Resource Usage**: Lower memory and CPU consumption in client applications

## Event Processing Pipeline

### Event Types
- **ADDED**: Resource creation events
- **UPDATED**: Resource modification events  
- **DELETED**: Resource deletion events (with tombstone handling)

### Processing Flow
1. **Event Reception**: Informers receive events from Kubernetes API
2. **Unified Handling**: Events processed through `handleUnifiedNormalizedEvent`
3. **Work Queue**: Events queued for asynchronous processing
4. **JSON Export**: Optional structured JSON output with timestamps

### Event Structure
```json
{
    "timestamp": "2025-09-10T11:15:27.945088151Z",
    "eventType": "ADDED",
    "gvr": "v1/configmaps",
    "namespace": "faro-test-2", 
    "name": "test-config-1",
    "uid": "9471c23c-0fea-4e17-902d-3e7588f6c205",
    "labels": {"app": "faro-test"}
}
```

## Initialization Sequence

1. **Client Setup**: Initialize Kubernetes dynamic and discovery clients
2. **Configuration Normalization**: Convert config to unified internal structure
3. **API Discovery**: Discover available resources (34 API groups, 395 resources)
4. **Informer Creation**: Create informers for each configured GVR with server-side filtering
5. **Cache Sync**: Wait for informer caches to sync
6. **Readiness Callback**: Trigger callback when fully initialized

## Readiness Management

### SetReadyCallback
```go
func (c *Controller) SetReadyCallback(callback func()) {
    c.readyMu.Lock()
    defer c.readyMu.Unlock()
    c.onReady = callback
    
    // Trigger callback if ready
    if c.isReady && callback != nil {
        callback()
    }
}
```

### IsReady
```go
func (c *Controller) IsReady() bool {
    c.readyMu.RLock()
    defer c.readyMu.RUnlock()
    return c.isReady
}
```

## Graceful Shutdown

The Controller implements comprehensive shutdown with timeout protection:

### Shutdown Sequence
1. **Context Cancellation**: Cancel main context to stop all informers
2. **Work Queue Shutdown**: Stop work queue to prevent new events
3. **Dynamic Informer Cleanup**: Cancel all dynamic informers explicitly
4. **Goroutine Synchronization**: Wait for all goroutines with timeout (25s)
5. **Resource Cleanup**: Ensure no resource leaks

### Implementation
```go
func (c *Controller) Stop() {
    c.logger.Info("controller", "Stopping multi-layered informer controller")
    
    // Cancel main context
    c.cancel()
    
    // Shutdown work queue
    c.workQueue.ShutDown()
    
    // Stop dynamic informers
    c.cancellers.Range(func(key, value interface{}) bool {
        if cancel, ok := value.(context.CancelFunc); ok {
            cancel()
        }
        return true
    })
    
    // Wait with timeout protection
    select {
    case <-done:
        c.logger.Info("controller", "All informers and workers stopped gracefully")
    case <-time.After(25 * time.Second):
        c.logger.Warning("controller", "Timeout waiting for informers to stop")
    }
}
```

## Configuration Integration

### Normalized Configuration Support
- Handles both namespace-centric and resource-centric configurations
- Converts all configurations to unified `NormalizedConfig` structure
- Supports multiple configurations per GVR with consolidated informers

### Resource Scope Handling
- **Namespace-scoped**: Resources like ConfigMaps, Secrets, Pods
- **Cluster-scoped**: Resources like Nodes, ClusterRoles, CustomResourceDefinitions
- Automatic scope detection through API discovery

## Error Handling

### Robust Error Management
- **Client Connection**: Graceful fallback from in-cluster to kubeconfig authentication
- **API Discovery**: Continues operation if some resources are unavailable
- **Informer Sync**: Timeout protection for cache synchronization
- **Event Processing**: Tombstone handling for deleted resources

### Logging Integration
- Structured logging with component identification
- Debug-level filtering configuration details
- Info-level operational status updates
- Warning-level timeout and error conditions

## Performance Characteristics

### Validated Performance
- **API Groups**: Successfully handles 34+ API groups
- **Resource Discovery**: Processes 395+ available resources
- **Event Processing**: No dropped or duplicate events across all test scenarios
- **Memory Efficiency**: Server-side filtering reduces client-side resource usage
- **Startup Time**: Consistent initialization across integration and E2E tests

### Scalability Features
- **Work Queue**: Rate-limited processing prevents API server overload
- **Informer Consolidation**: Single informer per GVR handles multiple configurations
- **Server-Side Filtering**: Reduces network traffic and client processing
- **Asynchronous Processing**: Non-blocking event handling through goroutines

## Testing Evidence

The Controller has been validated through comprehensive testing:

- **Unit Tests**: Configuration normalization and core functionality
- **Integration Tests**: Real Kubernetes cluster integration with callback synchronization
- **E2E Tests**: Full workflow validation across 8 different scenarios
- **Performance Tests**: Event processing pipeline validation with timing analysis

All tests demonstrate reliable initialization, accurate event processing, and clean shutdown behavior.