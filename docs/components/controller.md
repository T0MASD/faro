# Controller Component

The Controller is the core component of Faro that manages Kubernetes informers with a clean, unified architecture focused on **mechanisms, not policies**.

## Purpose

Provide reliable informer management and event streaming without business logic:

- **Informer Lifecycle**: Create, start, stop, and manage Kubernetes informers
- **Event Streaming**: Reliable event delivery via work queues
- **Server-side Filtering**: Efficient API-level resource filtering
- **JSON Export**: Structured event output for integration

## Architecture

### Clean Separation of Concerns

```
┌─────────────────────────────────────────────────────────────┐
│                    LIBRARY USERS                            │
│ • Event Handlers (Business Logic)                          │
│ • CRD Discovery                                             │
│ • Dynamic GVR Discovery                                     │
│ • Workload Processing                                       │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼ (Event Callbacks)
┌─────────────────────────────────────────────────────────────┐
│                    CONTROLLER                               │
│ • Informer Management                                       │
│ • Work Queue Processing                                     │
│ • Event Streaming                                           │
│ • JSON Export                                               │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼ (Kubernetes API)
┌─────────────────────────────────────────────────────────────┐
│                    KUBERNETES API                           │
└─────────────────────────────────────────────────────────────┘
```

## Key Features

### 1. Single Informer Creation Path
All informers are created through a unified path:

```go
// Single entry point for all informer creation
func (c *Controller) startUnifiedInformer(params InformerStartParams) {
    // Always use createNamespaceSpecificInformer for consistency
    informer, err := c.createNamespaceSpecificInformer(config, params.Namespace, params.NormalizedConfigs)
    // ... handle informer lifecycle
}
```

**Benefits**:
- **Consistent Behavior**: All informers follow the same patterns
- **Unified Lister Keys**: `GVRString@namespace` format for all resources
- **Simplified Debugging**: Single code path to understand and maintain

### 2. Pure Event-Driven Design
No timeouts or blocking operations:

```go
// Callback-based sync detection instead of blocking waits
func (c *Controller) setupSyncCallback(informer cache.SharedIndexInformer, description string) {
    var once sync.Once
    informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
        AddFunc: func(obj interface{}) {
            once.Do(func() {
                c.logger.Info("controller", description+" synced and ready")
            })
        },
    })
}
```

**Benefits**:
- **No Race Conditions**: Eliminates timing dependencies
- **Reliable Startup**: Informers signal when they're ready
- **Clean Shutdown**: Context-based cancellation without timeouts

### 3. Consistent Lister Key Strategy
All listers use the same key format:

```go
// Consistent key format for all resources
listerKey := config.GVRString + "@" + namespace

// Works for both namespace-scoped and cluster-scoped resources
// Namespace-scoped: "v1/configmaps@production"
// Cluster-scoped:   "v1/namespaces@"
```

**Benefits**:
- **Predictable Retrieval**: Always know how to find a lister
- **No Key Conflicts**: Unique keys for all informer combinations
- **Simplified Logic**: Single pattern for all resource types

## Core Methods

### Configuration-Driven Informers
```go
func (c *Controller) Start() error {
    // 1. Normalize configuration
    normalizedGVRs, err := c.config.Normalize()
    
    // 2. Start informers for configured resources
    return c.startConfigDrivenInformers()
}
```

### Dynamic Informer Management
```go
// Library users can add resources dynamically
func (c *Controller) AddResources(resources []ResourceConfig) error {
    // Add new resources to configuration
    // Start informers for new resources
}

func (c *Controller) StartInformers() error {
    // Start informers for newly added resources
}
```

### Event Handler Registration
```go
// Library users register business logic
func (c *Controller) AddEventHandler(handler EventHandler) {
    c.eventHandlers = append(c.eventHandlers, handler)
}

// Events are delivered to all registered handlers
func (c *Controller) handleUnifiedNormalizedEvent(eventType string, obj *unstructured.Unstructured, gvrString string, configs []NormalizedConfig) {
    for _, handler := range c.eventHandlers {
        handler.OnMatched(MatchedEvent{
            EventType: eventType,
            GVR:       gvrString,
            Object:    obj,
            Configs:   configs,
        })
    }
}
```

## Removed Business Logic

The Controller **no longer implements** the following business logic (moved to library users):

### 1. CRD Discovery
**Previously**: Automatic CRD watching and informer creation
```go
// REMOVED: Automatic CRD discovery
func (c *Controller) startCRDWatcher() error {
    // This functionality removed from core library
}
```

**Now**: Library users implement CRD discovery if needed:
```go
type CRDWatcher struct {
    controller *faro.Controller
}

func (c *CRDWatcher) OnMatched(event faro.MatchedEvent) error {
    if event.GVR == "apiextensions.k8s.io/v1/customresourcedefinitions" {
        // User implements CRD processing logic
        return c.processCRD(event.Object)
    }
    return nil
}
```

### 2. Special Event Processing
**Previously**: Hardcoded `v1/events` field extraction
```go
// REMOVED: Hardcoded involvedObject processing
if gvr == "v1/events" && processedObj != nil {
    if involvedObj, found, _ := unstructured.NestedMap(processedObj.Object, "involvedObject"); found {
        jsonEvent.InvolvedObject = involvedObj
    }
}
```

**Now**: Library users implement event processing via middleware:
```go
type EventProcessor struct{}

func (e *EventProcessor) OnMatched(event faro.MatchedEvent) error {
    if event.GVR == "v1/events" {
        // User extracts involvedObject and processes as needed
        return e.processEventForDiscovery(event.Object)
    }
    return nil
}
```

### 3. Workload Annotation Processing
**Previously**: Automatic workload annotation extraction
**Now**: Library users implement via event handlers:
```go
type WorkloadMiddleware struct{}

func (w *WorkloadMiddleware) OnMatched(event faro.MatchedEvent) error {
    // User implements workload detection and annotation
    workloadID := w.extractWorkloadID(event.Object.GetNamespace())
    
    // Add annotations as needed
    annotations := event.Object.GetAnnotations()
    if annotations == nil {
        annotations = make(map[string]string)
    }
    annotations["faro.workload.id"] = workloadID
    event.Object.SetAnnotations(annotations)
    
    return nil
}
```

## Error Handling Philosophy

### Strict Error Propagation
- **No Fallbacks**: Invalid configurations cause errors, not default behaviors
- **No Hidden Logic**: All functionality is explicit and visible
- **Fail Fast**: Problems surface immediately during startup

```go
// Example: Configuration errors are not masked
func (c *Config) Normalize() (map[string][]NormalizedConfig, error) {
    if len(normalizedMap) == 0 {
        return nil, fmt.Errorf("no resources configured")
    }
    return normalizedMap, nil
}
```

### No Deduplication Logic
- **Architectural Flaws Surface**: Duplicate informers indicate configuration problems
- **Clear Error Messages**: Users see exactly what's wrong
- **No Masking**: Problems aren't hidden by deduplication logic

## Performance Characteristics

### Efficiency
- **Server-side Filtering**: Kubernetes API handles filtering, not client-side
- **Namespace-scoped Informers**: Efficient per-namespace filtering
- **Single Informer per GVR+Namespace**: No duplicate informers

### Scalability  
- **Resource Usage**: Scales with configured resources, not cluster size
- **Memory Efficiency**: Minimal caching, efficient data structures
- **Event Processing**: Asynchronous work queue pattern

### Reliability
- **Context-based Shutdown**: Clean cancellation without timeouts
- **Thread Safety**: Proper synchronization with `sync.Map` and mutexes
- **Error Recovery**: Rate limiting and exponential backoff for failed events

## Integration Patterns

### Basic Usage
```go
// Simple informer management
controller := faro.NewController(client, logger, config)
controller.Start()
```

### Advanced Usage with Business Logic
```go
// Register multiple event handlers for different concerns
controller.AddEventHandler(&CRDWatcher{controller: controller})
controller.AddEventHandler(&WorkloadMonitor{controller: controller})
controller.AddEventHandler(&EventProcessor{controller: controller})

// Start with all business logic registered
controller.Start()
```

### Dynamic Resource Management
```go
// Add resources at runtime (library user implements discovery logic)
newResources := []faro.ResourceConfig{
    {GVR: "batch/v1/jobs", NamespaceNames: []string{"production"}},
}

controller.AddResources(newResources)
controller.StartInformers()
```

## Testing

### Unit Tests
- **Core Logic Only**: Test informer management without business logic
- **Mock Dependencies**: No Kubernetes cluster required
- **Fast Execution**: Focused on controller mechanisms

### Integration Tests
- **Real Kubernetes**: Validate against actual cluster
- **Business Logic**: Test library user implementations
- **Dynamic Scenarios**: Runtime informer creation and management

The Controller provides a **clean, reliable foundation** for Kubernetes resource monitoring while maintaining strict separation between mechanisms (provided by Faro) and policies (implemented by library users).