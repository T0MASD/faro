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
            GVR:               "v1/configmaps",
            Scope:             faro.NamespaceScope,
            NamespacePatterns: []string{".*"},
            NamePattern:       ".*",
            LabelPattern:      "app=^nginx-.*$",
        },
        {
            GVR:         "v1/services",
            Scope:       faro.NamespaceScope,
            NamePattern: ".*-service",
        },
    },
}
```

## Label Filtering

Faro supports two types of label filtering to provide flexible resource selection:

### Label Selector (Kubernetes Standard)

Uses Kubernetes-native label selector syntax for exact matching:

```go
config := &faro.Config{
    Resources: []faro.ResourceConfig{
        {
            GVR:           "v1/pods",
            Scope:         faro.NamespaceScope,
            LabelSelector: "app=nginx,environment=production",
        },
        {
            GVR:           "v1/services", 
            Scope:         faro.NamespaceScope,
            LabelSelector: "app in (nginx,apache)",
        },
        {
            GVR:           "v1/configmaps",
            Scope:         faro.NamespaceScope,
            LabelSelector: "app,!temp", // has 'app' label, doesn't have 'temp'
        },
    },
}
```

### Label Pattern (Regex Matching)

Client-side regex matching for complex patterns:

```go
config := &faro.Config{
    Resources: []faro.ResourceConfig{
        {
            GVR:          "v1/pods",
            Scope:        faro.NamespaceScope,
            LabelPattern: "app=^nginx-.*$", // matches nginx-web, nginx-api, etc.
        },
        {
            GVR:          "hypershift.openshift.io/v1beta1/hostedclusters",
            Scope:        faro.NamespaceScope,
            LabelPattern: "kubernetes.io/metadata.name=^ocm-staging-[a-z0-9]{32}-cs-ci-.*$",
        },
        {
            GVR:          "v1/configmaps",
            Scope:        faro.NamespaceScope,
            LabelPattern: "version=v\\d+\\.\\d+", // matches version=v1.2, v2.10, etc.
        },
    },
}
```

### Workload-Based Detection Pattern

Dynamic workload discovery with namespace-scoped efficiency:

```go
// WorkloadDetectionConfig demonstrates label-based workload discovery
type WorkloadDetectionConfig struct {
    DetectionLabel    string   // Label key to look for workloads
    WorkloadPattern   string   // Regex pattern to match workload names
    NamespacePattern  string   // Pattern to find related namespaces
}

config := WorkloadDetectionConfig{
    DetectionLabel:   "api.openshift.com/name",
    WorkloadPattern:  "toda-.*",
    NamespacePattern: "ocm-staging-(.+)",
}

// This enables:
// 1. Detection of namespaces with label "api.openshift.com/name" matching "toda-.*"
// 2. Extraction of workload ID from namespace name using pattern "ocm-staging-(.+)"
// 3. Dynamic creation of namespace-scoped informers for detected workloads
```

### YAML Configuration Examples

```yaml
# Namespace-centric format with label filtering
namespaces:
  - name_pattern: "^production-.*"
    resources:
      "v1/pods":
        name_pattern: ".*"
        label_selector: "app=nginx,tier=frontend"
      
      "v1/configmaps":
        name_pattern: ".*"
        label_pattern: "app=^web-.*$"

# Resource-centric format with label filtering  
resources:
  - gvr: "v1/pods"
    scope: "Namespaced"
    namespace_patterns: [".*"]
    name_pattern: ".*"
    label_selector: "app=nginx"
    
  - gvr: "v1/services"
    scope: "Namespaced" 
    namespace_patterns: ["production-.*"]
    name_pattern: ".*"
    label_pattern: "environment=^(prod|staging)$"
```

### Label Filtering vs Server-Side Filtering

**Label Selector (`label_selector`)**:
- ✅ Processed by Kubernetes API server (server-side filtering)
- ✅ Reduces network traffic 
- ✅ Better performance for large clusters
- ❌ Limited to Kubernetes label selector syntax

**Label Pattern (`label_pattern`)**:
- ✅ Full regex power for complex matching
- ✅ Can match any label value pattern
- ❌ Client-side filtering (all resources fetched first)
- ❌ Higher network overhead

### Combining Filters

You can combine multiple filtering criteria:

```go
config := &faro.Config{
    Resources: []faro.ResourceConfig{
        {
            GVR:               "v1/pods",
            Scope:             faro.NamespaceScope,
            NamespacePatterns: []string{"prod-.*", "stage-.*"},
            NamePattern:       "web-.*",
            LabelSelector:     "app=nginx", // Server-side pre-filter
            LabelPattern:      "version=^v[0-9]+\\.[0-9]+$", // Client-side regex
        },
    },
}
```

**Filter Processing Order:**
1. **Namespace patterns**: Filter namespaces to watch
2. **Label selector**: Server-side filtering (if specified)
3. **Name pattern**: Client-side resource name filtering  
4. **Label pattern**: Client-side label value regex filtering

### OCM Staging Use Case Example

For monitoring OpenShift CI/CD clusters in OCM staging:

```go
config := &faro.Config{
    Namespaces: []faro.NamespaceConfig{
        {
            // Monitor parent namespaces but filter resources by CI pattern
            NamePattern: "^ocm-staging-[a-z0-9]{32}$",
            Resources: map[string]faro.ResourceDetails{
                "hypershift.openshift.io/v1beta1/hostedclusters": {
                    NamePattern:  ".*",
                    LabelPattern: "kubernetes.io/metadata.name=^ocm-staging-[a-z0-9]{32}-cs-ci-[a-z0-9-]+$",
                },
                "hypershift.openshift.io/v1beta1/nodepools": {
                    NamePattern:  ".*", 
                    LabelPattern: "kubernetes.io/metadata.name=^ocm-staging-[a-z0-9]{32}-cs-ci-[a-z0-9-]+$",
                },
            },
        },
    },
}
```

This solves the timing problem by monitoring ALL parent namespaces but only logging resources that match the CI cluster pattern.

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

## Production Examples

### Workload Monitor (examples/workload-monitor.go)

The `workload-monitor` example demonstrates production-ready workload monitoring with advanced features:

#### Key Features
- **Dynamic Workload Detection**: Label-based discovery across cluster namespaces
- **Three-Tier Filtering**: Optimal resource efficiency through strategic GVR categorization
- **Namespace-Scoped Informers**: Per-workload monitoring for maximum efficiency
- **Structured Logging**: JSON output with workload context and metadata
- **Production Ready**: Command-line interface optimized for deployment scenarios

#### Usage Pattern
```bash
# Build the workload monitor
go build -o workload-monitor examples/workload-monitor.go

# Production workload monitoring
./workload-monitor \
  -label "api.openshift.com/name" \
  -pattern "toda-.*" \
  -namespace-pattern "ocm-staging-(.+)" \
  -allowedgvrs "v1/namespaces" \
  -workloadgvrs "v1/pods,v1/configmaps,v1/services,v1/secrets" \
  > workload.log 2>&1
```

#### Architecture Benefits
- **Efficiency**: Scales with workload count, not cluster size
- **Performance**: 95%+ reduction in processed events vs cluster-wide monitoring
- **Flexibility**: Configurable detection patterns and resource sets
- **Observability**: Structured JSON logs for analysis and alerting

#### Integration Example
```go
// Custom workload monitor integration
type CustomWorkloadMonitor struct {
    *WorkloadMonitor
    alertManager *AlertManager
    metrics      *MetricsCollector
}

func (c *CustomWorkloadMonitor) OnMatched(event faro.MatchedEvent) error {
    // Call base workload monitor
    if err := c.WorkloadMonitor.OnMatched(event); err != nil {
        return err
    }
    
    // Add custom alerting
    if event.EventType == "DELETED" && event.GVR == "v1/pods" {
        c.alertManager.SendAlert("Pod deleted", event)
    }
    
    // Update custom metrics
    c.metrics.RecordEvent(event.GVR, event.EventType)
    
    return nil
}
```

### Multi-Cluster Monitoring Pattern

```go
// MultiClusterMonitor demonstrates monitoring across multiple clusters
type MultiClusterMonitor struct {
    clusters map[string]*ClusterMonitor
    aggregator *EventAggregator
}

type ClusterMonitor struct {
    name       string
    controller *faro.Controller
    handler    *ClusterEventHandler
}

func (m *MultiClusterMonitor) AddCluster(name, kubeconfig string) error {
    // Create cluster-specific client
    client, err := faro.NewKubernetesClientFromConfig(kubeconfig)
    if err != nil {
        return err
    }
    
    // Create cluster-specific configuration
    config := &faro.Config{
        Resources: []faro.ResourceConfig{
            {
                GVR:   "v1/namespaces",
                Scope: faro.ClusterScope,
            },
            {
                GVR:               "v1/pods",
                Scope:             faro.NamespaceScope,
                NamespacePatterns: []string{"production-.*"},
            },
        },
    }
    
    logger, _ := faro.NewLogger(fmt.Sprintf("./logs/%s", name))
    controller := faro.NewController(client, logger, config)
    
    handler := &ClusterEventHandler{
        clusterName: name,
        aggregator:  m.aggregator,
    }
    controller.AddEventHandler(handler)
    
    m.clusters[name] = &ClusterMonitor{
        name:       name,
        controller: controller,
        handler:    handler,
    }
    
    return controller.Start()
}
```

### Performance Monitoring Integration

```go
// PerformanceMonitor demonstrates integration with metrics systems
type PerformanceMonitor struct {
    prometheus *prometheus.Registry
    counters   map[string]prometheus.Counter
    histograms map[string]prometheus.Histogram
}

func (p *PerformanceMonitor) OnMatched(event faro.MatchedEvent) error {
    // Record event metrics
    counterKey := fmt.Sprintf("%s_%s", event.GVR, event.EventType)
    if counter, exists := p.counters[counterKey]; exists {
        counter.Inc()
    }
    
    // Record processing latency
    processingTime := time.Since(event.Timestamp)
    if histogram, exists := p.histograms["processing_duration"]; exists {
        histogram.Observe(processingTime.Seconds())
    }
    
    // Forward to external monitoring system
    return p.forwardToMonitoringSystem(event)
}
```

## Best Practices

### Resource Efficiency
1. **Use Three-Tier Filtering**: Minimize cluster-wide monitoring, maximize namespace-scoped efficiency
2. **Validate Watchability**: Ensure GVRs support watch operations before creating informers
3. **Implement Graceful Shutdown**: Proper context cancellation and resource cleanup
4. **Monitor Performance**: Track event volumes and processing latency

### Production Deployment
1. **Structured Logging**: Use JSON output for analysis and alerting
2. **Error Handling**: Implement robust retry logic and error reporting
3. **Configuration Management**: Externalize configuration for different environments
4. **Health Checks**: Implement readiness and liveness probes

### Integration Patterns
1. **Event Handler Composition**: Layer multiple handlers for different concerns
2. **Workload Context**: Apply business logic based on workload detection
3. **Multi-Cluster Support**: Scale monitoring across cluster boundaries
4. **Metrics Integration**: Connect to observability platforms

This comprehensive guide covers patterns for using Faro as a library, from basic event handling to production workload monitoring architectures.