![banner](./faro.jpeg)

<div align="center">

[![License](https://img.shields.io/badge/License-Unlicense-blue.svg)](https://unlicense.org/)

</div>

# Faro - Kubernetes Resource Monitoring Library

[![Go Reference](https://pkg.go.dev/badge/github.com/T0MASD/faro.svg)](https://pkg.go.dev/github.com/T0MASD/faro)

**Go library for monitoring Kubernetes resource changes** with server-side filtering, JSON export, and readiness callbacks.

> 📚 **Library-First**: Import into your Go applications for Kubernetes resource monitoring
> 🔧 **Features**: Server-side filtering (exact matching), JSON export, readiness callbacks, graceful shutdown
> 🚀 **Simple**: `go get github.com/T0MASD/faro`

## Why Faro?

| **Feature** | **Faro Library** | **kubectl get --watch** | **Custom Controllers** |
|-------------|------------------|-------------------------|------------------------|
| **Server-side Filtering** | ✅ Exact matching + label selectors | ❌ Basic selectors | ⚠️ Manual implementation |
| **JSON Export** | ✅ Structured event output | ❌ Text only | ⚠️ Custom serialization |
| **Readiness Callbacks** | ✅ Programmatic notification | ❌ No readiness signal | ⚠️ Custom implementation |
| **Graceful Shutdown** | ✅ Clean resource cleanup | ❌ Process termination | ⚠️ Manual handling |
| **Dynamic Informers** | ✅ Runtime creation | ❌ Static resources | ✅ Full control |
| **Event Handlers** | ✅ Callback interface | ❌ Not applicable | ✅ Full control |

## System Overview

**Purpose**: Go library for Kubernetes resource monitoring with examples for specialized use cases.

**Key Features**:
- **Server-side Filtering**: Exact matching via Kubernetes API (no client-side filtering)
- **JSON Export**: Structured event output for integration and analysis
- **Readiness Callbacks**: Programmatic notification when controller is ready
- **Graceful Shutdown**: Clean resource cleanup on termination
- **Event Handlers**: Callback interface for custom event processing
- **Dynamic Informers**: Runtime creation based on configuration

## Architecture

### Core Library Components

- **Configuration**: YAML-driven resource and namespace configuration
- **Controller**: Multi-layered informer management with readiness callbacks
- **Filtering**: Server-side exact matching only (no client-side filtering)
- **Events**: JSON export and callback-based event handling
- **Lifecycle**: Graceful startup and shutdown with proper cleanup

### Processing Flow

**Core Library:**
```
Config Load → API Discovery → Informer Creation → Readiness Callback → Event Processing → JSON Export
```

**Event Processing:**
```
Resource Change → Kubernetes Informer → Work Queue → Worker Goroutines → Event Handler Callbacks → Application Filtering
                        ↑                                                                              ↑
                [Server-side filtering]                                                    [Application event handlers]
                (exact names, labels, field selectors)                                    (custom filtering, business logic)
```

## Configuration Approaches

### Core Library Configuration
YAML-based configuration with server-side filtering only:
```yaml
# Enable JSON export
json_export: true
output_dir: "./logs"

# Monitor specific namespaces and resources
namespaces:
  - name_pattern: "production"                    # Server-side exact match only
    resources:
      "v1/pods":
        name_pattern: "web-server"                # Server-side exact match only
        label_selector: "app=nginx,tier=frontend" # Server-side filtering
      "v1/configmaps":
        name_pattern: "app-config"                # Server-side exact match
        # name_pattern: "prod-.*"                 # Would cause WARNING + no filtering

# Monitor resources across all namespaces
resources:
  - gvr: "v1/configmaps"
    scope: "namespace"
    namespace_patterns: ["production"]           # Server-side exact match
    name_pattern: "app-config"                   # Server-side exact match
    label_selector: "app=web"                    # Server-side filtering
```

### Library Usage
Basic library integration:
```go
// Load configuration
config, err := faro.LoadConfig()
if err != nil {
    log.Fatal(err)
}

// Create controller (note: logger must be created with config for proper log level control)
client, _ := faro.NewKubernetesClient()
logger, _ := faro.NewLogger(config)  // Log level from config is configured here
controller := faro.NewController(client, logger, config)

// Register event handler
controller.AddEventHandler(&MyHandler{})

// Start monitoring
controller.Start()
```

## Core Features

### Log Level Control
Faro provides comprehensive log level control:

```go
// Clean logs - debug messages hidden from console
config := &faro.Config{
    LogLevel: "info",    // Only info, warning, error messages shown
    // ... other config
}

// Verbose logs - all messages including debug shown  
config := &faro.Config{
    LogLevel: "debug",   // All messages including debug shown
    // ... other config
}

// Logger creation configures the log level to klog verbosity
logger, err := faro.NewLogger(config)  // klog verbosity configured here
```

**Key Points:**
- **Log level is configured during logger creation** - not just stored in config
- **klog verbosity is automatically configured** based on the log level
- **Debug messages only appear when LogLevel is "debug"**
- **All log levels are written to files** regardless of console verbosity
- **JSON export always works** regardless of console log level

### Filtering Architecture
Faro implements **server-side filtering only** through the Kubernetes API:

#### **Server-side Filtering** (Kubernetes API level)
- **Exact namespace filtering**: `metadata.namespace=exact-name` field selectors
- **Exact name filtering**: `metadata.name=exact-name` field selectors (no wildcards/regex)
- **Label selectors**: Standard Kubernetes label selector syntax (`app=nginx,tier=frontend`)

#### **No Client-side Filtering**
Faro core does **not** implement client-side pattern matching or regex filtering. When regex patterns are configured:
- Faro logs a **warning**: "Regex name patterns not supported for server-side filtering"
- **No filtering is applied** - all events for that resource type are received
- Applications must implement their own filtering in event handlers if needed

**Important**: 
- Kubernetes API does not support regex or wildcards in field selectors
- Patterns containing `.*+?^${}[]|()\\` result in **no filtering** and a warning
- Use exact matches for efficient server-side filtering
- Implement custom filtering in your event handlers for complex patterns

### JSON Export
Structured event output for integration:
```json
{
  "timestamp": "2025-01-10T10:30:45.123456789Z",
  "eventType": "ADDED",
  "gvr": "v1/configmaps",
  "namespace": "default",
  "name": "app-config",
  "uid": "12345678-1234-1234-1234-123456789012",
  "labels": {"app": "web", "version": "v1.0"}
}
```

### Readiness Callbacks
Programmatic notification when controller is ready:
```go
controller := faro.NewController(client, logger, config)

// Set callback for when controller is ready
controller.SetReadyCallback(func() {
    log.Println("Faro controller is ready")
    // Start your application logic here
})

// Check readiness status
if controller.IsReady() {
    // Controller is ready for processing
}
```

### Graceful Shutdown
Clean resource cleanup:
- Stops all informers and workers
- Drains work queues
- Closes file handles and connections
- Returns when all resources are cleaned up

## Advanced Filtering System 🎯

### **Faro-Aligned GVR Filtering** (Workload Monitor)
Optimal resource efficiency through scope-based filtering:

#### **Cluster GVRs** (Cluster-Wide)
- ✅ **Minimal cluster monitoring** - only essential cluster-scoped resources
- ✅ **Workload detection** - typically `v1/namespaces`
- ✅ **Low resource usage** - single informers for detection
- ✅ **Explicit inclusion** - no complex allowlist/denylist logic

#### **Namespace GVRs** (Per-Namespace)
- ✅ **Namespace-scoped informers** - created dynamically per workload
- ✅ **Server-side filtering** - only events from detected namespaces
- ✅ **High efficiency** - scales with workloads, not cluster size
- ✅ **Faro alignment** - matches library's scope-based configuration

### **Label-Based Workload Detection**
```bash
# Detect workloads by label and namespace pattern
-label "api.openshift.com/name" -pattern "toda-.*" -workload-id-pattern "ocm-staging-(.+)"
```

### **Faro Core Filtering** (Library)
- **Label Selector**: Server-side Kubernetes filtering (`app=nginx,tier=frontend`)
- **Exact Name Matching**: Server-side field selector filtering (`metadata.name=exact-name`)
- **No Pattern Matching**: Regex patterns result in warnings and no filtering (applications must implement custom filtering)

## Event Processing

### Core Library
- **Work Queue Pattern**: Standard Kubernetes controller pattern with rate limiting
- **Multi-Level Filtering**: Namespace → labels → name patterns
- **Event Correlation**: Consistent key-based resource identification
- **Error Handling**: Exponential backoff with maximum retry limits

### Application Event Processing Patterns

**Event Handler Implementation:**
- **Application Filtering**: Applications implement filtering logic in event handlers for complex patterns and business rules
- **Worker Dispatchers**: Can be set up to process matched events and take further actions on resource activity:
  - Create, update, or delete related resources
  - Send notifications or alerts
  - Update external systems or databases
  - Trigger workflows or automation
  - Apply business logic based on resource state

**Example Worker Dispatcher Pattern:**
```go
type ResourceWorkerDispatcher struct {
    client kubernetes.Interface
}

func (d *ResourceWorkerDispatcher) OnMatched(event faro.MatchedEvent) error {
    // Additional application filtering
    if !d.shouldProcess(event) {
        return nil
    }
    
    // Take action on the resource
    switch event.EventType {
    case "ADDED":
        return d.handleResourceCreation(event.Object)
    case "UPDATED":
        return d.handleResourceUpdate(event.Object)
    case "DELETED":
        return d.handleResourceDeletion(event.Object)
    }
    return nil
}
```

### Workload Monitor Enhancement
- **Dynamic Informer Creation**: Namespace-scoped informers created per detected workload
- **Application-Level Filtering**: Workload context applied to all resource events in application handlers
- **Structured Logging**: JSON output with workload metadata and context
- **Efficient Scaling**: Resource usage scales with workloads, not cluster size

## Technical Features

### Discovery and Monitoring
- **API Resource Discovery**: Runtime enumeration of 395 cluster API resources from 34 API groups
- **CRD Detection**: Real-time CustomResourceDefinition monitoring for dynamic informer creation
- **Scope Detection**: Automatic cluster vs namespace-scoped resource identification
- **Watchability Validation**: Filters resources by 'watch' verb support, excludes problematic resources

### Performance and Reliability  
- **Server-side Filtering**: Exact matching for maximum efficiency; no client-side filtering overhead
- **Work Queue Pattern**: 3 worker goroutines with rate limiting and exponential backoff
- **Informer Deduplication**: Single informer per GVR regardless of configuration overlap
- **Graceful Shutdown**: Context-based cancellation with proper resource cleanup
- **Thread-Safe Operations**: `sync.Map` and proper synchronization primitives

### Observability
- **Structured Logging**: Key-value logging with configurable levels (debug, info, warning, error)
- **Log Level Control**: klog verbosity configuration ensures debug messages only appear when requested
- **Event Prefixing**: Clear `CONFIG [EVENT_TYPE]` prefixes for filtered events
- **Async Processing**: Non-blocking log operations with channel-based queueing
- **Auto-Shutdown**: Configurable timeout for testing and automation scenarios

## Real-World Use Cases 🚀

### **Basic Resource Monitoring**
Monitor specific resources in namespaces:
```yaml
# Monitor ConfigMaps in specific namespaces
namespaces:
  - name_pattern: "production"                # Server-side exact match
    resources:
      "v1/configmaps":
        label_selector: "app=nginx"           # Server-side filtering
      "v1/pods":
        name_pattern: "web-server"            # Server-side exact match
        # name_pattern: "web-.*"              # Would cause WARNING + no filtering
```

### **Multi-Resource Monitoring**
Monitor multiple resource types with different filters:
```yaml
resources:
  - gvr: "v1/pods"
    scope: "Namespaced"
    namespace_patterns: ["default", "kube-system"]  # Server-side exact match
    label_selector: "app=nginx"                     # Server-side filtering
  - gvr: "v1/namespaces"
    scope: "Cluster"
    name_pattern: "production"                      # Server-side exact match
    # name_pattern: "prod-.*"                       # Would cause WARNING + no filtering
```

## Usage

### Core Library
```go
package main

import (
    "context"
    "log"
    
    faro "github.com/T0MASD/faro/pkg"
)

func main() {
    // Load configuration
    config := &faro.Config{}
    if err := config.LoadFromYAML("config.yaml"); err != nil {
        log.Fatal(err)
    }
    
    // Create Kubernetes client and logger
    client, err := faro.NewKubernetesClient()
    if err != nil {
        log.Fatal(err)
    }
    
    logger, err := faro.NewLogger(config.GetLogDir())
    if err != nil {
        log.Fatal(err)
    }
    defer logger.Shutdown()
    
    // Create controller
    controller := faro.NewController(client, logger, config)
    
    // Set readiness callback
    controller.SetReadyCallback(func() {
        log.Println("Faro controller is ready")
    })
    
    // Add event handler (optional)
    controller.AddEventHandler(&MyEventHandler{})
    
    // Start controller
    if err := controller.Start(); err != nil {
        log.Fatal(err)
    }
}

type MyEventHandler struct{}

func (h *MyEventHandler) OnMatched(event faro.MatchedEvent) error {
    log.Printf("Event: %s %s %s/%s", 
        event.EventType, event.GVR, event.Object.GetNamespace(), event.Object.GetName())
    return nil
}
```

### Examples
```bash
# Build and run basic CLI tool
go build -o faro main.go
./faro -config config.yaml

# Build and run library usage example
go build -o library-example examples/library-usage.go
./library-example
```

### Core Library (Custom Applications)
```go
import "github.com/T0MASD/faro/pkg"

// Load config and create components
config, _ := faro.LoadFromYAML("config.yaml")
client, _ := faro.NewKubernetesClient()
logger, _ := faro.NewLogger(config.GetLogDir())
controller := faro.NewController(client, logger, config)

// Register event handler for custom processing
controller.AddEventHandler(&MyHandler{})

// Start monitoring
controller.Start()
```

### CLI Tool (Basic Monitoring)
```bash
# Build and run core CLI
make build
./faro --config config.yaml
```

### Development and Testing
```bash
# Build and test
make build               # Build faro binary
make build-dev           # Build with dev version info
make test-ci             # Run CI-safe tests (unit tests only, no K8s required)
make test-unit           # Unit tests only (no K8s required)
make test                # Run all tests (requires K8s cluster)
make test-e2e            # E2E tests (requires K8s cluster)
make test-integration    # Integration tests (requires K8s cluster)

# Release management (triggers GitHub Actions)
make tag-patch           # Create patch version tag and trigger release
make tag-minor           # Create minor version tag and trigger release
make tag-major           # Create major version tag and trigger release
```

### GitHub Actions Workflows

The project includes comprehensive GitHub Actions workflows for automated testing and log collection:

#### CI Workflow (`.github/workflows/ci.yml`)
- **Triggers**: Push to `main`/`develop` branches, pull requests
- **Features**:
  - Runs full test suite (unit, integration, E2E)
  - Uploads test logs and JSON events as artifacts with 30-day retention
  - Matrix testing with different log levels
  - Automatic log level analysis (Debug vs Info message counts)
  - Event analysis (event types, counts, samples)
  - Collects logs and events from all test directories

#### Release Workflow (`.github/workflows/release.yml`)
- **Triggers**: Version tags (`v*`)
- **Features**:
  - Runs comprehensive test suite before release
  - Uploads release test logs and JSON events with 90-day retention
  - Creates GitHub releases with GoReleaser

#### Manual Test Log Workflow (`.github/workflows/test-logs.yml`)
- **Triggers**: Manual dispatch with configurable parameters
- **Features**:
  - Configurable log level (debug, info, warning, error)
  - Configurable test duration
  - Detailed log level and event analysis with reporting
  - Uploads workload monitor logs, JSON events, unit test logs, and summary reports

#### Artifact Structure
All workflows use the [GitHub Actions upload-artifact@v4](https://github.com/actions/upload-artifact) action to upload test logs:

```yaml
- name: Upload test logs
  uses: actions/upload-artifact@v4
  with:
    name: test-logs-${{ github.run_number }}-${{ github.sha }}
    path: test-logs/
    retention-days: 30
```

**Artifact Types**:
- `test-logs-*`: Complete test execution logs and JSON events with analysis
- `workload-monitor-logs-*`: Workload monitor specific logs and events
- `e2e-logs-*`: End-to-end test logs and events
- `integration-logs-*`: Integration test logs and events
- `release-test-logs-*`: Release validation logs and events
- `test-summary-*`: Workflow execution summaries

**Analysis Features**: Each artifact includes automated analysis of:
- **Log Levels**: Debug (`^D`) vs info (`^I`) message counts and percentages
- **Events**: JSON event counts, event type distribution, and sample events
- **Verification**: Confirms that the logging level fix works correctly (debug messages show as `D`, not `I`)
- **GitHub Actions Integration**: Uses `.github/scripts/analyze-test-logs.sh` for consistent analysis across all workflows

#### Analysis Scripts

The project includes reusable analysis scripts for both local development and CI/CD:

**`.github/scripts/analyze-test-logs.sh`** - GitHub Actions optimized script:
- Generates JSON and Markdown reports
- Sets GitHub Actions outputs for workflow integration
- Validates logging behavior with PASS/FAIL status
- Integrates with GitHub Actions step summaries

**`scripts/test-log-analysis.sh`** - Local development script:
- Simulates GitHub Actions analysis locally
- Provides immediate feedback during development
- Generates comprehensive analysis reports

**Usage Examples:**
```bash
# Local development
./scripts/test-log-analysis.sh debug 30

# GitHub Actions (used automatically in workflows)
./.github/scripts/analyze-test-logs.sh debug 30 output-dir
```

### Installation

```bash
go get github.com/T0MASD/faro
```

### Quick Start

```go
package main

import (
    "context"
    "log"
    "github.com/T0MASD/faro/pkg"
)

func main() {
    // Create Kubernetes client
    client, err := faro.NewKubernetesClient()
    if err != nil {
        log.Fatal(err)
    }
    
    // Create configuration (must be created before logger for proper log level control)
    config := &faro.Config{
        OutputDir: "./output",
        LogLevel:  "info",  // Controls console verbosity: "debug" shows all messages, "info" hides debug messages
        JsonExport: true,
        Resources: []faro.ResourceConfig{
            {
                Group:     "",
                Version:   "v1",
                Resource:  "configmaps",
                Namespace: "default",
            },
        },
    }
    
    // Create logger with configuration (log level is configured during logger creation)
    logger, err := faro.NewLogger(config)
    if err != nil {
        log.Fatal(err)
    }
    defer logger.Shutdown()
    
    // Create and start controller
    controller := faro.NewController(client, logger, config)
    
    // Set readiness callback (optional)
    controller.SetReadyCallback(func() {
        logger.Info("main", "Faro is ready and monitoring resources")
    })
    
    // Start monitoring
    ctx := context.Background()
    if err := controller.Start(ctx); err != nil {
        logger.Error("main", "Controller error: "+err.Error())
    }
}
```

### Build Example CLI from Source

```bash
git clone https://github.com/T0MASD/faro.git
cd faro
make build  # Creates example CLI for testing
```

## Documentation

- [Architecture Overview](docs/architecture.md) - Core library design and implementation details
- [Component Reference](docs/components/) - Detailed component documentation
  - [Client](docs/components/client.md) - Kubernetes API client management
  - [Config](docs/components/config.md) - Configuration processing and validation  
  - [Logger](docs/components/logger.md) - Multi-handler logging system
- [Comprehensive Analysis](COMPREHENSIVE_FARO_ANALYSIS.md) - Complete architecture analysis with test validation

## Examples

- **workload-monitor.go** - Workload monitoring with dynamic detection
- **library-usage.go** - Basic library integration patterns
- **worker-dispatcher.go** - Advanced event processing with worker dispatchers for taking actions on resource activity
- **E2E Tests** - Comprehensive validation suite for library and examples
