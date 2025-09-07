![banner](./faro.jpeg)

<div align="center">

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

</div>

# Faro

**Kubernetes Resource Monitoring Library** with advanced filtering, dynamic workload detection, and production-ready monitoring capabilities.

> 🎯 **Perfect for**: Workload lifecycle monitoring, CI/CD pipeline tracking, multi-cluster observability, and custom resource management
> 📚 **Library-First**: Core Kubernetes monitoring with examples for specialized use cases like workload-monitor

## Why Faro?

| **Feature** | **Faro Library** | **kubectl get --watch** | **Custom Controllers** |
|-------------|------------------|-------------------------|------------------------|
| **Library-First Design** | ✅ Go library + examples | ❌ CLI only | ⚠️ Framework-based |
| **Advanced Filtering** | ✅ Allowlist/Denylist/Workload GVRs | ❌ Basic selectors | ⚠️ Manual implementation |
| **Dynamic Workload Detection** | ✅ Label-based + namespace patterns | ❌ Static resources | ⚠️ Custom logic required |
| **Namespace-Scoped Informers** | ✅ Per-workload efficiency | ❌ Cluster-wide only | ⚠️ Complex setup |
| **Production Monitoring** | ✅ Workload-monitor example | ❌ Not suitable | ✅ Full control |
| **Resource Efficiency** | ✅ Server-side + client-side filtering | ❌ All events processed | ⚠️ DIY optimization |

## System Overview

**Purpose**: Go library for Kubernetes resource monitoring with specialized examples for production workload tracking. Core library provides foundational monitoring capabilities while examples like `workload-monitor` demonstrate advanced use cases.

**Key Characteristics**:
- **Library-First**: Core Kubernetes monitoring library with production examples
- **Advanced Filtering**: Three-tier filtering (allowedgvrs/deniedgvrs/workloadgvrs)
- **Dynamic Workload Detection**: Label-based workload discovery with namespace pattern matching
- **Namespace-Scoped Efficiency**: Per-workload informers for optimal resource usage
- **Production Examples**: workload-monitor for real-world deployment scenarios
- **Comprehensive Coverage**: 400+ Kubernetes GVRs with watchability validation

## Architecture

### Core Library Components

- **Configuration Layer**: YAML-driven configuration with flexible GVR filtering
- **Discovery Engine**: Runtime API enumeration with watchability validation (400+ GVRs)
- **Controller Architecture**: Multi-layered informer management with dynamic creation
- **Event Processing**: Work queue pattern with exponential backoff and graceful shutdown
- **Filtering System**: Three-tier filtering (allowed/denied/workload GVRs)
- **Library Interface**: Event handler callbacks for custom monitoring applications

### Workload Monitor Example

- **Dynamic Detection**: Label-based workload discovery across cluster namespaces
- **Namespace-Scoped Informers**: Per-workload resource monitoring for efficiency
- **Advanced Filtering**: Allowlist (cluster-wide) + Workload GVRs (per-namespace)
- **Production Ready**: Structured logging with workload context and metadata

### Processing Flow

**Core Library:**
```
Config Load → API Discovery → Informer Creation → Event Detection → Work Queue → Event Handlers
```

**Workload Monitor Example:**
```
Workload Detection → Namespace Discovery → Dynamic Informer Creation → Resource Filtering → Structured Logging
```

## Configuration Approaches

### Core Library Configuration
YAML-based configuration for foundational monitoring:
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

### Workload Monitor Configuration
Command-line driven for production workload monitoring:
```bash
./workload-monitor \
  -label "api.openshift.com/name" \
  -pattern "toda-.*" \
  -namespace-pattern "ocm-staging-(.+)" \
  -allowedgvrs "v1/namespaces" \
  -workloadgvrs "v1/pods,v1/configmaps,v1/services,v1/secrets"
```

**Three-Tier Filtering:**
- **allowedgvrs**: Cluster-wide monitoring (e.g., namespaces for workload detection)
- **deniedgvrs**: Explicitly excluded GVRs (reduces noise)
- **workloadgvrs**: Per-namespace monitoring for detected workloads (efficiency)

## Advanced Filtering System 🎯

### **Three-Tier GVR Filtering** (Workload Monitor)
Optimal resource efficiency through strategic filtering:

#### **Allowed GVRs** (Cluster-Wide)
- ✅ **Minimal cluster monitoring** - only essential resources
- ✅ **Workload detection** - typically just `v1/namespaces`
- ✅ **Low resource usage** - single informers for detection

#### **Workload GVRs** (Per-Namespace)
- ✅ **Namespace-scoped informers** - created dynamically per workload
- ✅ **Server-side filtering** - only events from detected namespaces
- ✅ **High efficiency** - scales with workloads, not cluster size

#### **Denied GVRs** (Explicit Exclusion)
- ✅ **Noise reduction** - exclude high-volume, low-value resources
- ✅ **Performance optimization** - prevent unnecessary processing
- ✅ **Customizable** - adapt to cluster characteristics

### **Label-Based Workload Detection**
```bash
# Detect workloads by label and namespace pattern
-label "api.openshift.com/name" -pattern "toda-.*" -namespace-pattern "ocm-staging-(.+)"
```

### **Traditional Label Filtering** (Core Library)
- **Label Selector**: Server-side Kubernetes filtering (`app=nginx,tier=frontend`)
- **Label Pattern**: Client-side regex matching (`version=^v[0-9]+\\.[0-9]+$`)

## Event Processing

### Core Library
- **Work Queue Pattern**: Standard Kubernetes controller pattern with rate limiting
- **Multi-Level Filtering**: Namespace → labels → name patterns
- **Event Correlation**: Consistent key-based resource identification
- **Error Handling**: Exponential backoff with maximum retry limits

### Workload Monitor Enhancement
- **Dynamic Informer Creation**: Namespace-scoped informers created per detected workload
- **Client-Side Filtering**: Workload context applied to all resource events
- **Structured Logging**: JSON output with workload metadata and context
- **Efficient Scaling**: Resource usage scales with workloads, not cluster size

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

### **Production Workload Monitoring** (workload-monitor)
Monitor dynamic workloads across Management and Service clusters:
```bash
# Management Cluster - comprehensive workload tracking
./workload-monitor \
  -label "api.openshift.com/name" \
  -pattern "toda-.*" \
  -namespace-pattern "ocm-staging-(.+)" \
  -allowedgvrs "v1/namespaces" \
  -workloadgvrs "v1/pods,v1/configmaps,v1/services,apps/v1/deployments,hypershift.openshift.io/v1beta1/hostedclusters"

# Service Cluster - focused application monitoring  
./workload-monitor \
  -label "api.openshift.com/name" \
  -pattern "toda-.*" \
  -namespace-pattern "ocm-staging-(.+)" \
  -allowedgvrs "v1/namespaces" \
  -workloadgvrs "v1/pods,v1/configmaps,v1/services,apps/v1/deployments"
```

### **CI/CD Pipeline Monitoring** (Core Library)
Monitor OpenShift CI clusters with complex naming patterns:
```yaml
namespaces:
  - name_pattern: "^ocm-staging-[a-z0-9]{32}$"
    resources:
      "hypershift.openshift.io/v1beta1/hostedclusters":
        name_pattern: ".*"
        label_pattern: "kubernetes.io/metadata.name=^ocm-staging-[a-z0-9]{32}-cs-ci-.*$"
```

### **Multi-Cluster Observability**
Track workload lifecycle across different cluster types:
- **Workload Detection**: Label-based discovery (`api.openshift.com/name`)
- **Namespace Patterns**: Dynamic namespace matching (`ocm-staging-(.+)`)
- **Resource Efficiency**: Per-workload informers vs cluster-wide monitoring
- **Structured Output**: JSON logs with workload context for analysis

## Usage

### Workload Monitor (Production Ready)
```bash
# Build workload monitor
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
# Run library examples
make examples             # All examples
make example-library      # Basic usage
make example-worker       # Worker dispatcher

# Run comprehensive E2E tests
make test-e2e            # Validates library + workload-monitor
```

## Documentation

- [Architecture Overview](docs/architecture.md) - Core library + workload monitor design
- [Library Usage Guide](docs/library-usage.md) - Go library examples and integration patterns
- [Audit Requirements](docs/audit-requirements.md) - Production monitoring validation criteria
- [Component Reference](docs/components/) - Detailed component documentation
  - [Client](docs/components/client.md) - Kubernetes API client management
  - [Config](docs/components/config.md) - Configuration processing and validation  
  - [Controller](docs/components/controller.md) - Event handler interface and informer lifecycle
  - [Logger](docs/components/logger.md) - Callback-based logging system

## Examples

- **workload-monitor.go** - Production workload monitoring with dynamic detection
- **library-usage.go** - Basic library integration patterns
- **worker-dispatcher.go** - Advanced event processing patterns
- **E2E Tests** - Comprehensive validation suite for library and examples
