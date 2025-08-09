![banner](./faro.jpeg)

<div align="center">

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

</div>

# Faro

Kubernetes resource observation tool with dynamic discovery and configuration-driven informer management, was rapidly developed in a single day. The project's efficiency is a testament to the power of meticulous prompt engineering.

## System Overview

**Purpose**: Monitor Kubernetes resource lifecycle events (ADDED/UPDATED/DELETED) across namespaced and cluster-scoped resources using dynamic informer creation.

**Key Characteristics**:
- Configuration-driven informer creation
- Real-time API discovery and CRD monitoring
- Work queue-based event processing with rate limiting
- Dual configuration format support (namespace-centric and resource-centric)
- Server-side label selector filtering
- Regex pattern matching for resource and namespace names

## Architecture

### Core Components

- **Configuration Layer**: Dual YAML format support with normalization to unified internal structure
- **Discovery Engine**: Runtime API enumeration with automatic scope detection and CRD monitoring  
- **Controller Architecture**: Multi-layered informer management with work queue pattern
- **Event Processing**: Asynchronous processing with exponential backoff and graceful shutdown
- **Logging System**: Structured async logging with overflow protection and dual output streams

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
      "v1/configmaps":
        name_pattern: "app-config"
        label_selector: "env=production"
```

### Resource-Centric  
Monitor specific resource types across namespace patterns:
```yaml
resources:
  - gvr: "v1/configmaps"
    scope: "Namespaced"
    namespace_patterns: ["prod-.*", "staging-.*"]
    name_pattern: "app-.*"
    label_selector: "managed-by=faro"
```

## Event Processing

- **Work Queue Pattern**: Standard Kubernetes controller pattern with rate limiting
- **Filtering Logic**: Multi-level filtering (namespace patterns, name patterns, label selectors)
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

## Build and Test

```bash
# Build
make build

# Run E2E tests
make test-e2e

# Development setup
make dev-setup
```

## Documentation

- [Architecture Overview](docs/architecture.md) - Complete system design
- [Component Reference](docs/components/) - Detailed component documentation
  - [Client](docs/components/client.md) - Kubernetes API client management
  - [Config](docs/components/config.md) - Configuration processing and validation
  - [Controller](docs/components/controller.md) - Informer lifecycle management
  - [Logger](docs/components/logger.md) - Async logging system
