![banner](./faro.jpeg)

<div align="center">

[![License](https://img.shields.io/badge/License-Unlicense-blue.svg)](https://unlicense.org/)

</div>

# Faro - Kubernetes Resource Monitoring Library

[![Go Reference](https://pkg.go.dev/badge/github.com/T0MASD/faro.svg)](https://pkg.go.dev/github.com/T0MASD/faro)

**Clean Go library for Kubernetes resource monitoring** - provides mechanisms, not policies.

> 📚 **Library-First**: Pure mechanisms for Kubernetes resource monitoring
> 🔧 **Clean Architecture**: Library provides tools, users implement business logic  
> 🚀 **Simple**: `go get github.com/T0MASD/faro`

## Why Faro?

| **Feature** | **Faro Library** | **kubectl get --watch** | **Custom Controllers** |
|-------------|------------------|-------------------------|------------------------|
| **Server-side Filtering** | ✅ Exact matching + label selectors | ❌ Basic selectors | ⚠️ Manual implementation |
| **JSON Export** | ✅ Structured event output | ❌ Text only | ⚠️ Custom serialization |
| **Readiness Callbacks** | ✅ Programmatic notification | ❌ No readiness signal | ⚠️ Custom implementation |
| **Graceful Shutdown** | ✅ Clean resource cleanup | ❌ Process termination | ⚠️ Manual handling |
| **Dynamic Informers** | ✅ Runtime creation | ❌ Static resources | ✅ Full control |
| **Clean Architecture** | ✅ Mechanisms only | ❌ Not applicable | ⚠️ Mixed concerns |

## Philosophy: Mechanisms, Not Policies

**Faro Core Provides:**
- ✅ **Informer Management**: Create, start, stop Kubernetes informers
- ✅ **Event Streaming**: Reliable event delivery with work queues
- ✅ **Server-side Filtering**: Efficient API-level resource filtering
- ✅ **JSON Export**: Structured event output for integration
- ✅ **Lifecycle Management**: Graceful startup, readiness, shutdown

**Library Users Implement:**
- 🔧 **Business Logic**: CRD discovery, workload detection, annotation processing
- 🔧 **Configuration Interpretation**: Complex selectors, patterns, rules
- 🔧 **Event Processing**: Filtering, correlation, actions, workflows
- 🔧 **Integration Logic**: External systems, notifications, automation

## Architecture

### Core Library (Mechanisms)
```
Simple Config → Informer Creation → Event Streaming → JSON Export
     ↓                ↓                   ↓             ↓
[Basic YAML]    [K8s Informers]    [Work Queues]  [Structured Output]
```

### Library Users (Policies)
```
Business Config → Dynamic Discovery → Event Processing → Actions
      ↓                 ↓                   ↓             ↓
[Complex Rules]   [CRD Watching]    [Custom Filtering] [Workflows]
```

## Configuration

Faro supports **simple configuration formats** - complex interpretation is left to library users:

### Namespace Format
```yaml
# Simple namespace-centric configuration
output_dir: "./logs"
json_export: true

namespaces:
  - name_pattern: "production"
    resources:
      "v1/configmaps":
        label_selector: "app=nginx"
      "batch/v1/jobs": {}
```

### Resource Format  
```yaml
# Simple resource-centric configuration
output_dir: "./logs"
json_export: true

resources:
  - gvr: "v1/configmaps"
    namespace_names: ["production", "staging"]
    label_selector: "app=nginx"
  - gvr: "batch/v1/jobs"
    namespace_names: ["production"]
```

## Basic Usage

### Core Library Integration
```go
package main

import (
    "log"
    faro "github.com/T0MASD/faro/pkg"
)

func main() {
    // Load simple configuration
    config, err := faro.LoadConfig()
    if err != nil {
        log.Fatal(err)
    }

    // Create components
    client, _ := faro.NewKubernetesClient()
    logger, _ := faro.NewLogger(config)
    controller := faro.NewController(client, logger, config)

    // Register event handler (implement your business logic here)
    controller.AddEventHandler(&MyEventHandler{})

    // Start monitoring
    controller.Start()
}

type MyEventHandler struct{}

func (h *MyEventHandler) OnMatched(event faro.MatchedEvent) error {
    // Implement your business logic here:
    // - Custom filtering
    // - Workload detection  
    // - Annotation processing
    // - External integrations
    log.Printf("Event: %s %s %s/%s", 
        event.EventType, event.GVR, event.Object.GetNamespace(), event.Object.GetName())
    return nil
}
```

## Advanced Usage Examples

### 1. Workload Monitor (Business Logic Implementation)
See `examples/workload-monitor.go` - demonstrates how library users implement:
- **Dynamic GVR Discovery**: Extract GVRs from `v1/events` 
- **Workload Detection**: Identify workloads from namespace patterns
- **Annotation Processing**: Add workload metadata to events
- **Namespace Monitoring**: Watch for new workload namespaces

### 2. CRD Discovery (Business Logic Implementation)  
Library users can implement CRD discovery:
```go
type CRDWatcher struct {
    controller *faro.Controller
}

func (c *CRDWatcher) OnMatched(event faro.MatchedEvent) error {
    if event.GVR == "apiextensions.k8s.io/v1/customresourcedefinitions" {
        // Extract GVR from CRD
        // Add to controller configuration
        // Start new informers
        return c.handleCRDEvent(event)
    }
    return nil
}
```

### 3. Event-Driven Discovery (Business Logic Implementation)
Library users can implement dynamic discovery:
```go
type EventProcessor struct {
    controller *faro.Controller
}

func (e *EventProcessor) OnMatched(event faro.MatchedEvent) error {
    if event.GVR == "v1/events" {
        // Extract involvedObject GVR
        // Dynamically add new resources to monitoring
        return e.processEventForDiscovery(event)
    }
    return nil
}
```

## Core Features

### Server-side Filtering
- **Exact Matching**: `metadata.name=exact-name` field selectors
- **Label Selectors**: Standard Kubernetes syntax (`app=nginx,tier=frontend`)
- **Namespace Filtering**: Efficient per-namespace informers

### JSON Export
Structured event output:
```json
{
  "timestamp": "2025-01-18T10:30:45Z",
  "eventType": "ADDED",
  "gvr": "v1/configmaps",
  "namespace": "default",
  "name": "app-config",
  "uid": "12345678-1234-1234-1234-123456789012",
  "labels": {"app": "web", "version": "v1.0"}
}
```

### Lifecycle Management
- **Readiness Callbacks**: Know when controller is ready
- **Graceful Shutdown**: Clean resource cleanup
- **Error Handling**: Proper error propagation (no fallbacks)

## Testing

### Unit Tests (No Kubernetes Required)
```bash
make test-unit
```

### Integration Tests (Kubernetes Required)
```bash
make test-integration  # Tests library user implementations
```

### E2E Tests (Kubernetes Required)
```bash
make test-e2e         # Tests core library functionality
```

### All Tests
```bash
make test             # Runs all test suites
```

## Installation

```bash
go get github.com/T0MASD/faro
```

## Documentation

- [Architecture Overview](docs/architecture.md) - Clean library design principles
- [Component Reference](docs/components/) - Core component documentation
  - [Controller](docs/components/controller.md) - Informer management
  - [Config](docs/components/config.md) - Simple configuration formats
- [Examples](examples/) - Real implementations of business logic

## Examples

- **library-usage.go** - Basic library integration
- **workload-monitor.go** - Dynamic workload detection (business logic)
- **worker-dispatcher.go** - Event processing and actions (business logic)

## Key Principles

1. **Library provides mechanisms** - informers, events, JSON export
2. **Users implement policies** - business logic, complex filtering, workflows  
3. **No fallbacks or defaults** - errors are surfaced, not hidden
4. **Clean separation** - core library vs. application concerns
5. **Simple configuration** - complex interpretation left to users

**Faro gives you the tools. You build the solutions.**