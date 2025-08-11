# Faro Library Usage

Guide for using Faro as a Go library in your applications.

## Basic Library Usage

### Setup and Initialization

```go
package main

import (
    "log"
    "os"
    "os/signal"
    "syscall"
    
    faro "github.com/T0MASD/faro/pkg"
)

func main() {
    // 1. Create configuration programmatically
    config := &faro.Config{
        OutputDir:  "./logs",
        LogLevel:   "info",
        Resources: []faro.ResourceConfig{
            {
                GVR:               "v1/configmaps",
                Scope:             faro.NamespaceScope,
                NamespacePatterns: []string{"default", "kube-system"},
                NamePattern:       ".*",
            },
            {
                GVR:         "v1/namespaces", 
                Scope:       faro.ClusterScope,
                NamePattern: ".*test.*",
            },
        },
    }
    
    // 2. Create components
    client, err := faro.NewKubernetesClient()
    if err != nil {
        log.Fatalf("Failed to create Kubernetes client: %v", err)
    }
    
    logger, err := faro.NewLogger("./logs")
    if err != nil {
        log.Fatalf("Failed to create logger: %v", err)
    }
    defer logger.Shutdown()
    
    controller := faro.NewController(client, logger, config)
    
    // 3. Register event handlers
    controller.AddEventHandler(&MyEventHandler{})
    
    // 4. Start monitoring
    if err := controller.Start(); err != nil {
        log.Fatalf("Failed to start controller: %v", err)
    }
    
    // 5. Wait for shutdown signal
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    <-sigChan
    
    controller.Stop()
}
```

### Event Handler Implementation

```go
type MyEventHandler struct {
    name string
}

func (h *MyEventHandler) OnMatched(event faro.MatchedEvent) error {
    fmt.Printf("[%s] Event: %s %s %s\n",
        h.name,
        event.EventType,
        event.GVR,
        event.Key)
    
    // Access full Kubernetes object
    if event.Object != nil {
        labels := event.Object.GetLabels()
        annotations := event.Object.GetAnnotations()
        
        fmt.Printf("  Labels: %v\n", labels)
        fmt.Printf("  Annotations: %v\n", annotations)
        
        // Resource-specific processing
        switch event.GVR {
        case "v1/configmaps":
            processConfigMap(event)
        case "v1/namespaces":
            processNamespace(event)
        }
    }
    
    return nil
}

func processConfigMap(event faro.MatchedEvent) {
    if data, found, _ := unstructured.NestedStringMap(event.Object.Object, "data"); found {
        fmt.Printf("  ConfigMap data keys: %v\n", getKeys(data))
    }
}

func processNamespace(event faro.MatchedEvent) {
    if phase, found, _ := unstructured.NestedString(event.Object.Object, "status", "phase"); found {
        fmt.Printf("  Namespace phase: %s\n", phase)
    }
}
```

## Configuration Loading

### From YAML File

```go
// Load from external YAML file
config := &faro.Config{}
if err := config.LoadFromYAML("config.yaml"); err != nil {
    log.Fatalf("Failed to load config: %v", err)
}

// Create components with loaded config
client, _ := faro.NewKubernetesClient()
logger, _ := faro.NewLogger(config.GetLogDir())
controller := faro.NewController(client, logger, config)
```

### Programmatic Configuration

```go
config := &faro.Config{
    OutputDir:       "./output",
    LogLevel:        "debug",
    AutoShutdownSec: 0,
    Resources: []faro.ResourceConfig{
        {
            GVR:               "v1/pods",
            Scope:             faro.NamespaceScope,
            NamespacePatterns: []string{"production-.*", "staging-.*"},
            NamePattern:       "web-.*",
            LabelSelector:     "app=nginx",
        },
        {
            GVR:         "v1/services",
            Scope:       faro.NamespaceScope,
            NamePattern: ".*-service",
        },
    },
}
```

## Advanced Usage: Worker Dispatcher Pattern

### Dispatcher Architecture

For complex applications, use the worker dispatcher pattern to route events to specialized handlers:

```go
// ResourceWorker interface for handling specific resource types
type ResourceWorker interface {
    HandleEvent(ctx context.Context, event faro.MatchedEvent) error
    Handles() []string  // GVRs this worker handles
    Name() string
}

// WorkerDispatcher manages multiple resource-specific workers
type WorkerDispatcher struct {
    workers  map[string]ResourceWorker
    workChan chan faro.MatchedEvent
    ctx      context.Context
    cancel   context.CancelFunc
    wg       sync.WaitGroup
}
```

### Dispatcher Implementation

```go
func NewWorkerDispatcher() *WorkerDispatcher {
    ctx, cancel := context.WithCancel(context.Background())
    return &WorkerDispatcher{
        workers:  make(map[string]ResourceWorker),
        workChan: make(chan faro.MatchedEvent, 1000),
        ctx:      ctx,
        cancel:   cancel,
    }
}

func (wd *WorkerDispatcher) RegisterWorker(worker ResourceWorker) {
    // Map GVRs to workers
    for _, gvr := range worker.Handles() {
        wd.workers[gvr] = worker
    }
    
    // Start worker goroutine
    wd.wg.Add(1)
    go wd.runWorker(worker)
}

func (wd *WorkerDispatcher) runWorker(worker ResourceWorker) {
    defer wd.wg.Done()
    
    for {
        select {
        case <-wd.ctx.Done():
            return
        case event := <-wd.workChan:
            // Route to appropriate worker
            for _, gvr := range worker.Handles() {
                if gvr == event.GVR {
                    if err := worker.HandleEvent(wd.ctx, event); err != nil {
                        log.Printf("Worker %s failed: %v", worker.Name(), err)
                    }
                    break
                }
            }
        }
    }
}

// Implement faro.EventHandler interface
func (wd *WorkerDispatcher) OnMatched(event faro.MatchedEvent) error {
    select {
    case wd.workChan <- event:
        return nil
    default:
        return fmt.Errorf("worker dispatcher channel full")
    }
}
```

### Resource-Specific Workers

#### ConfigMap Worker

```go
type ConfigMapWorker struct{}

func (cw *ConfigMapWorker) Name() string {
    return "configmap-worker"
}

func (cw *ConfigMapWorker) Handles() []string {
    return []string{"v1/configmaps"}
}

func (cw *ConfigMapWorker) HandleEvent(ctx context.Context, event faro.MatchedEvent) error {
    fmt.Printf("📋 [ConfigMap] %s %s\n", event.EventType, event.Key)
    
    switch event.EventType {
    case "ADDED":
        return cw.handleConfigMapAdded(event)
    case "UPDATED":
        return cw.handleConfigMapUpdated(event)
    case "DELETED":
        return cw.handleConfigMapDeleted(event)
    }
    
    return nil
}

func (cw *ConfigMapWorker) handleConfigMapAdded(event faro.MatchedEvent) error {
    // Extract ConfigMap data
    if data, found, _ := unstructured.NestedStringMap(event.Object.Object, "data"); found {
        fmt.Printf("  📝 New ConfigMap with %d keys\n", len(data))
        
        // Process specific configurations
        for key, value := range data {
            if key == "config.yaml" {
                fmt.Printf("  🔧 Found config file: %s\n", key)
                // Parse and validate configuration
            }
        }
    }
    
    return nil
}

func (cw *ConfigMapWorker) handleConfigMapUpdated(event faro.MatchedEvent) error {
    fmt.Printf("  🔄 ConfigMap updated\n")
    // Detect configuration changes and trigger reloads
    return nil
}

func (cw *ConfigMapWorker) handleConfigMapDeleted(event faro.MatchedEvent) error {
    fmt.Printf("  🗑️ ConfigMap deleted\n")
    // Clean up cached configurations
    return nil
}
```

#### Pod Worker

```go
type PodWorker struct{}

func (pw *PodWorker) Name() string {
    return "pod-worker"
}

func (pw *PodWorker) Handles() []string {
    return []string{"v1/pods"}
}

func (pw *PodWorker) HandleEvent(ctx context.Context, event faro.MatchedEvent) error {
    fmt.Printf("🚀 [Pod] %s %s\n", event.EventType, event.Key)
    
    switch event.EventType {
    case "ADDED":
        return pw.handlePodScheduled(event)
    case "UPDATED":
        return pw.handlePodStatusChange(event)
    case "DELETED":
        return pw.handlePodTerminated(event)
    }
    
    return nil
}

func (pw *PodWorker) handlePodScheduled(event faro.MatchedEvent) error {
    // Extract pod phase and node assignment
    phase, _, _ := unstructured.NestedString(event.Object.Object, "status", "phase")
    nodeName, _, _ := unstructured.NestedString(event.Object.Object, "spec", "nodeName")
    
    fmt.Printf("  📊 Phase: %s, Node: %s\n", phase, nodeName)
    
    // Trigger deployment tracking or log aggregation setup
    return nil
}

func (pw *PodWorker) handlePodStatusChange(event faro.MatchedEvent) error {
    // Monitor container statuses and readiness
    if conditions, found, _ := unstructured.NestedSlice(event.Object.Object, "status", "conditions"); found {
        for _, condition := range conditions {
            if condMap, ok := condition.(map[string]interface{}); ok {
                condType, _, _ := unstructured.NestedString(condMap, "type")
                status, _, _ := unstructured.NestedString(condMap, "status")
                
                if condType == "Ready" && status == "True" {
                    fmt.Printf("  ✅ Pod is ready\n")
                    // Start monitoring or health checks
                }
            }
        }
    }
    
    return nil
}

func (pw *PodWorker) handlePodTerminated(event faro.MatchedEvent) error {
    fmt.Printf("  ⚰️ Pod terminated\n")
    // Clean up monitoring resources
    return nil
}
```

### Complete Worker Dispatcher Usage

```go
func main() {
    // Create Faro components
    config := createConfiguration()
    client, _ := faro.NewKubernetesClient()
    logger, _ := faro.NewLogger("./logs")
    controller := faro.NewController(client, logger, config)
    
    // Create worker dispatcher
    dispatcher := NewWorkerDispatcher()
    
    // Register specialized workers
    dispatcher.RegisterWorker(&ConfigMapWorker{})
    dispatcher.RegisterWorker(&PodWorker{})
    dispatcher.RegisterWorker(&NamespaceWorker{})
    dispatcher.RegisterWorker(&ServiceWorker{})
    
    // Connect Faro to dispatcher
    controller.AddEventHandler(dispatcher)
    
    // Start monitoring
    controller.Start()
    
    // Graceful shutdown
    defer func() {
        controller.Stop()
        dispatcher.Shutdown()
        logger.Shutdown()
    }()
    
    // Wait for signals
    waitForShutdown()
}
```

## Event Handler Patterns

### Multiple Handler Registration

```go
// Register multiple handlers for different purposes
controller.AddEventHandler(&LoggingHandler{})
controller.AddEventHandler(&MetricsHandler{})
controller.AddEventHandler(&AlertingHandler{})
controller.AddEventHandler(&WorkerDispatcher{})
```

### Conditional Processing

```go
type ConditionalHandler struct {
    environment string
}

func (ch *ConditionalHandler) OnMatched(event faro.MatchedEvent) error {
    // Only process events in specific environments
    if labels := event.Object.GetLabels(); labels != nil {
        if env, exists := labels["environment"]; exists && env == ch.environment {
            return ch.processEvent(event)
        }
    }
    
    return nil // Skip event
}
```

### Error Handling and Retry

```go
type RobustHandler struct {
    maxRetries int
}

func (rh *RobustHandler) OnMatched(event faro.MatchedEvent) error {
    for attempt := 0; attempt < rh.maxRetries; attempt++ {
        if err := rh.processEvent(event); err != nil {
            log.Printf("Attempt %d failed for %s: %v", attempt+1, event.Key, err)
            
            if attempt == rh.maxRetries-1 {
                return fmt.Errorf("max retries exceeded for %s", event.Key)
            }
            
            // Exponential backoff
            time.Sleep(time.Duration(1<<attempt) * time.Second)
            continue
        }
        
        return nil // Success
    }
    
    return nil
}
```

## Testing Patterns

### Mock Event Handler

```go
type MockEventHandler struct {
    events []faro.MatchedEvent
    mu     sync.Mutex
}

func (meh *MockEventHandler) OnMatched(event faro.MatchedEvent) error {
    meh.mu.Lock()
    defer meh.mu.Unlock()
    meh.events = append(meh.events, event)
    return nil
}

func (meh *MockEventHandler) GetEvents() []faro.MatchedEvent {
    meh.mu.Lock()
    defer meh.mu.Unlock()
    return append([]faro.MatchedEvent{}, meh.events...)
}

func (meh *MockEventHandler) Reset() {
    meh.mu.Lock()
    defer meh.mu.Unlock()
    meh.events = nil
}
```

### Test Setup

```go
func TestFaroIntegration(t *testing.T) {
    // Create test configuration
    config := &faro.Config{
        OutputDir: t.TempDir(),
        LogLevel:  "debug",
        Resources: []faro.ResourceConfig{
            {
                GVR:   "v1/configmaps",
                Scope: faro.NamespaceScope,
                NamespacePatterns: []string{"test-.*"},
            },
        },
    }
    
    // Create components
    client, err := faro.NewKubernetesClient()
    require.NoError(t, err)
    
    logger, err := faro.NewLogger("")
    require.NoError(t, err)
    defer logger.Shutdown()
    
    controller := faro.NewController(client, logger, config)
    
    // Register mock handler
    mockHandler := &MockEventHandler{}
    controller.AddEventHandler(mockHandler)
    
    // Start controller
    err = controller.Start()
    require.NoError(t, err)
    defer controller.Stop()
    
    // Create test resources and verify events
    // ... test implementation
}
```

## Performance Considerations

### Handler Goroutines

Event handlers are called in separate goroutines to prevent blocking:

```go
// In controller.processObject():
for _, handler := range handlers {
    go func(h faro.EventHandler, event faro.MatchedEvent) {
        if err := h.OnMatched(event); err != nil {
            c.logger.Warning("controller", fmt.Sprintf("Handler failed: %v", err))
        }
    }(handler, matchedEvent)
}
```

### Channel Buffering

For high-throughput applications, use buffered channels in workers:

```go
// Large buffer for high event volumes
workChan: make(chan faro.MatchedEvent, 10000)
```

### Resource Cleanup

Always implement proper cleanup:

```go
func (wd *WorkerDispatcher) Shutdown() {
    wd.cancel()           // Cancel context
    close(wd.workChan)    // Close channel
    wd.wg.Wait()          // Wait for workers
}
```

## Complete Example

See `examples/library-usage.go` and `examples/worker-dispatcher.go` for runnable examples.

### Running Examples

```bash
# Option 1: Using Make targets (recommended)
make example-library      # Basic library usage
make example-worker       # Worker dispatcher pattern
make examples             # Run all examples sequentially

# Option 2: Direct go run (no go.mod needed)
cd examples && go run library-usage.go
cd examples && go run worker-dispatcher.go

# Test with resources while examples are running
kubectl create configmap test-cm --from-literal=key=value
kubectl create namespace test-namespace
kubectl delete configmap test-cm
kubectl delete namespace test-namespace
```

## Integration with Existing Applications

### Embedding in Services

```go
type MyService struct {
    faro     *faro.Controller
    handlers []faro.EventHandler
}

func (s *MyService) Start() error {
    // Initialize Faro as part of service startup
    config := s.loadFaroConfig()
    client, _ := faro.NewKubernetesClient()
    logger, _ := faro.NewLogger(config.GetLogDir())
    
    s.faro = faro.NewController(client, logger, config)
    
    // Register service-specific handlers
    for _, handler := range s.handlers {
        s.faro.AddEventHandler(handler)
    }
    
    return s.faro.Start()
}

func (s *MyService) Stop() {
    if s.faro != nil {
        s.faro.Stop()
    }
}
```

This guide covers patterns for using Faro as a library, from basic event handling to worker dispatcher architectures.