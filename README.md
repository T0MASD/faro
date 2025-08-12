![banner](./faro.jpeg)

<div align="center">

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

</div>

# Faro

**Smart Kubernetes resource monitoring** with dual label filtering, dynamic discovery, and flexible configuration formats.

> 🎯 **Perfect for**: CI/CD pipeline monitoring, production workload tracking, security compliance, and custom resource management

## Why Faro?

| **Feature** | **Faro** | **kubectl get --watch** | **Custom Controllers** |
|-------------|----------|-------------------------|------------------------|
| **Configuration-Driven** | ✅ YAML configs | ❌ CLI only | ❌ Code-based |
| **Dual Label Filtering** | ✅ Server + Regex | ❌ Basic only | ⚠️ Manual implementation |
| **Multi-Resource** | ✅ Single config | ❌ One command per resource | ⚠️ Complex setup |
| **CRD Auto-Discovery** | ✅ Real-time | ❌ Manual | ⚠️ Manual implementation |
| **Go Library** | ✅ Event handlers | ❌ Not available | ✅ Full control |
| **Production Ready** | ✅ Rate limiting, graceful shutdown | ❌ Basic watching | ⚠️ DIY reliability |

## System Overview

**Purpose**: Monitor Kubernetes resource lifecycle events (ADDED/UPDATED/DELETED) across namespaced and cluster-scoped resources using dynamic informer creation. Available as both CLI tool and Go library.

**Key Characteristics**:
- **Smart Filtering**: Dual label filtering (Kubernetes selectors + regex patterns)
- **Configuration-Driven**: Namespace-centric and resource-centric YAML formats
- **Real-Time Discovery**: API discovery and CRD monitoring with dynamic informer creation
- **Production-Ready**: Work queue processing with rate limiting and graceful shutdown
- **High Performance**: Server-side filtering for efficiency, client-side regex for flexibility
- **Developer-Friendly**: Go library interface with event handler callbacks

## Architecture

### Core Components

- **Configuration Layer**: Dual YAML format support with normalization to unified internal structure
- **Discovery Engine**: Runtime API enumeration with automatic scope detection and CRD monitoring  
- **Controller Architecture**: Multi-layered informer management with work queue pattern
- **Event Processing**: Asynchronous processing with exponential backoff and graceful shutdown
- **Logging System**: Callback-based logging with pluggable handlers
- **Library Interface**: Event handler registration for external consumption

### Processing Flow

```
Config Load → API Discovery → Informer Creation → Event Detection → Work Queue → Processing → Logging
```

## Configuration Formats

### Namespace-Centric
Monitor specific namespaces and their contained resources:
```yaml
namespaces:
  - name_pattern: "prod-.*"
    resources:
      "v1/pods":
        name_pattern: "web-.*"
        label_selector: "app=nginx,tier=frontend"  # Server-side filtering
      "v1/configmaps":
        name_pattern: ".*"
        label_pattern: "app=^web-.*$"             # Regex pattern matching
```

### Resource-Centric  
Monitor specific resource types across namespace patterns:
```yaml
resources:
  - gvr: "v1/pods"
    scope: "Namespaced"
    namespace_patterns: ["prod-.*", "staging-.*"]
    name_pattern: "web-.*"
    label_selector: "app=nginx"                   # Pre-filter for performance
    label_pattern: "version=^v[0-9]+\\.[0-9]+$"   # Regex for semantic versions
```

## Smart Label Filtering 🎯

Faro provides two complementary label filtering approaches for optimal performance and flexibility:

### **Label Selector** (Kubernetes Standard)
- ✅ **Server-side filtering** - reduces network traffic
- ✅ **High performance** for large clusters  
- ✅ **Standard syntax**: `app=nginx,tier=frontend`

### **Label Pattern** (Regex Matching)
- ✅ **Full regex power** for complex patterns
- ✅ **Flexible matching** - `app=^web-.*$`, `version=v\\d+\\.\\d+`
- ✅ **Perfect for CI/CD** naming patterns and version matching

### **Combined Usage**
```yaml
# Best of both worlds: pre-filter + refine
resources:
  - gvr: "v1/pods"
    label_selector: "app=nginx"              # Server-side pre-filter
    label_pattern: "version=^v[0-9]+\\.[0-9]+$"  # Client-side regex refinement
```

## Event Processing

- **Work Queue Pattern**: Standard Kubernetes controller pattern with rate limiting
- **Smart Filtering**: Multi-level filtering (namespace → labels → name patterns)
- **Event Correlation**: Consistent key-based resource identification
- **Error Handling**: Exponential backoff with maximum retry limits

## Technical Features

### Discovery and Monitoring
- **API Resource Discovery**: Runtime enumeration of 400+ cluster API resources
- **CRD Detection**: Real-time CustomResourceDefinition monitoring for dynamic informer creation
- **Scope Detection**: Automatic cluster vs namespace-scoped resource identification
- **Multi-Version Support**: Handles multiple API versions for same resource types

### Performance and Reliability  
- **Informer Deduplication**: Single informer per GVR regardless of configuration overlap
- **Graceful Shutdown**: Context-based cancellation with proper resource cleanup
- **Memory Efficiency**: Shared informer factories with optimized caching
- **Race Condition Prevention**: Atomic operations and proper synchronization

### Observability
- **Structured Logging**: Key-value logging with configurable levels (debug, info, warning, error)
- **Event Prefixing**: Clear `CONFIG [EVENT_TYPE]` prefixes for filtered events
- **Async Processing**: Non-blocking log operations with channel-based queueing
- **Auto-Shutdown**: Configurable timeout for testing and automation scenarios

## Real-World Use Cases 🚀

### **CI/CD Pipeline Monitoring**
Monitor OpenShift CI clusters with complex naming patterns:
```yaml
namespaces:
  - name_pattern: "^ocm-staging-[a-z0-9]{32}$"
    resources:
      "hypershift.openshift.io/v1beta1/hostedclusters":
        name_pattern: ".*"
        label_pattern: "kubernetes.io/metadata.name=^ocm-staging-[a-z0-9]{32}-cs-ci-.*$"
```

### **Production Workload Tracking**
Monitor specific application versions across environments:
```yaml
resources:
  - gvr: "v1/pods"
    namespace_patterns: ["prod-.*", "staging-.*"]
    label_selector: "app=nginx"                    # Performance pre-filter
    label_pattern: "version=^v[2-9]\\.[0-9]+$"     # Only v2.x+ versions
```

### **Security Compliance**
Track resources without required security labels:
```yaml
resources:
  - gvr: "v1/secrets"
    label_selector: "!security-reviewed"          # Missing security label
  - gvr: "v1/configmaps"
    label_pattern: "classification=^(public|internal|confidential)$"
```

## Usage

### CLI Tool
```bash
# Build and run
make build
./faro --config config.yaml

# Quick example configs
./faro --config examples/ocm-staging.yaml     # CI/CD monitoring
./faro --config examples/production.yaml     # Production workloads
```

### Go Library
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

### Examples
```bash
# Run library examples
make examples             # All examples
make example-library      # Basic usage
make example-worker       # Worker dispatcher

# Run all E2E tests (includes library tests)
make test-e2e
```

## Documentation

- [Architecture Overview](docs/architecture.md) - Complete system design
- [Library Usage Guide](docs/library-usage.md) - Comprehensive Go library examples and patterns
- [Component Reference](docs/components/) - Detailed component documentation
  - [Client](docs/components/client.md) - Kubernetes API client management
  - [Config](docs/components/config.md) - Configuration processing and validation
  - [Controller](docs/components/controller.md) - Event handler interface and informer lifecycle
  - [Logger](docs/components/logger.md) - Callback-based logging system
