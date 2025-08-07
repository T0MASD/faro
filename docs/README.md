# Faro: Kubernetes Workload Analyzer

**Version:** 1.0.0  
**Codename:** Project Faro  
**Date:** August 2025  

## Overview

Faro is a high-fidelity Kubernetes resource analyzer that captures chronological records of all relevant events and state changes during any Kubernetes resource's lifecycle. It generates structured JSON files containing complete "resource stories" suitable for offline debugging and AI/ML analysis.

**Faro can watch absolutely any Kubernetes resource** - from built-in resources like Pods, Deployments, and Services, to custom resources (CRDs) like Operators, Controllers, or any custom workload. Whether you're debugging a simple Pod deployment or a complex multi-resource Operator, Faro captures the complete story.

## Problem Statement

Debugging complex, transient, or performance-related issues in Kubernetes resources is challenging because:

- Resource lifecycle involves cascading events across multiple related resources (Pods, Events, Services, CRDs, etc.)
- Logs are scattered across different components and namespaces
- It's difficult to reconstruct the exact sequence of events after the fact
- Understanding *why* a resource's lifecycle was slow, failed, or behaved unexpectedly requires correlating multiple data sources

## Solution

Faro addresses these challenges by:

1. **Capturing High-Fidelity Event Streams**: Watches any Kubernetes resource and its related components simultaneously
2. **Correlating Related Events**: Uses correlation IDs to group events from the same resource lifecycle
3. **Generating Structured Output**: Produces chronological JSON files with complete event history
4. **Supporting AI/ML Analysis**: Output format optimized for automated analysis

## Architecture

### System Components

```
┌─────────────────────────────────────────────────────────────┐
│                    External Environment                     │
│              (CI Runner, Developer Machine)                │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐    │
│  │              Collector Agent (Go App)              │    │
│  │  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐  │    │
│  │  │   Watchers  │ │Worker Pool  │ │ Correlation │  │    │
│  │  │             │ │             │ │   Engine    │  │    │
│  │  └─────────────┘ └─────────────┘ └─────────────┘  │    │
│  └─────────────────────────────────────────────────────┘    │
│                           │                                │
└───────────────────────────│────────────────────────────────┘
                            │ (Kubeconfig API Connection)
                            ▼
┌─────────────────────────────────────────────────────────────┐
│              Kubernetes Cluster API Server                 │
│                                                             │
│  Watches:                                                  │
│  ├── Any Kubernetes Resource                              │
│  │   ├── Built-in Resources (Pods, Deployments, etc.)    │
│  │   ├── Custom Resources (CRDs, Operators, etc.)        │
│  │   └── Events (v1.Event)                               │
│  └── Container Logs (Streams)                             │
└─────────────────────────────────────────────────────────────┘
```

### Data Flow

1. **Event Collection**: Multiple watchers monitor Kubernetes resources
2. **Event Processing**: Worker pool processes events asynchronously
3. **Correlation**: Events are grouped by correlation ID
4. **Output Generation**: Complete correlations are written to JSON files

## Features

### Core Capabilities

- **Universal Resource Watching**: Watches any Kubernetes resource - built-in or custom
- **Container Log Streaming**: Captures container logs when pods enter Running state
- **Event Correlation**: Groups related events using correlation IDs
- **Asynchronous Processing**: Non-blocking event processing with worker pools
- **Structured Output**: Generates chronological JSON files with complete event history

### Observability

- **Structured Logging**: Detailed logs with correlation IDs
- **Runtime Statistics**: Events processed per minute, active correlations, memory usage
- **Health Monitoring**: Built-in health checks and metrics
- **Graceful Shutdown**: Clean termination of all watchers and workers

### Scalability

- **Worker Pool Pattern**: Configurable number of worker goroutines
- **Buffered Channels**: Prevents backpressure during high event volumes
- **Memory Management**: Efficient event buffering with configurable limits
- **Resource Limits**: Configurable limits for log streams and correlations

## Installation

### Prerequisites

- Go 1.21 or later
- Kubernetes cluster access
- `kubectl` configured with cluster access

### Building from Source

```bash
# Clone the repository
git clone <repository-url>
cd faro

# Build the binary
go build -o bin/faro cmd/faro/main.go

# Or use make
make build
```

### Running

```bash
# Watch a Deployment
./bin/faro --resource="apps/v1/Deployment" --namespace="default"

# Watch a specific Deployment by name
./bin/faro --resource="apps/v1/Deployment" --name="my-app" --namespace="production"

# Watch a custom resource (CRD)
./bin/faro --resource="mycompany.com/v1/MyCustomResource" --namespace="default"

# Watch a Pod directly
./bin/faro --resource="v1/Pod" --name="my-pod" --namespace="default"

# Watch with custom output directory
./bin/faro --resource="apps/v1/Deployment" --output="./correlations" --namespace="default"

# Watch multiple resources (comma-separated)
./bin/faro --resource="apps/v1/Deployment,apps/v1/Service" --namespace="default"
```

## Configuration

### Command Line Options

| Option | Description | Default | Required |
|--------|-------------|---------|----------|
| `--resource` | Kubernetes resource type(s) to watch (e.g., "apps/v1/Deployment", "v1/Pod") | - | Yes |
| `--name` | Specific resource name to watch | - | No |
| `--namespace` | Target namespace | "default" | No |
| `--output` | Output directory for JSON files | "./output" | No |
| `--kubeconfig` | Path to kubeconfig file | - | No |
| `--workers` | Number of worker goroutines | 5 | No |
| `--buffer-size` | Event channel buffer size | 1000 | No |
| `--log-level` | Log level (debug, info, warn, error) | "info" | No |
| `--metrics-port` | Metrics server port | "8080" | No |
| `--correlation-timeout` | Correlation completion timeout | "5m" | No |
| `--include-logs` | Include container logs in output | true | No |
| `--max-log-streams` | Maximum number of concurrent log streams | 50 | No |

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `FARO_LOG_LEVEL` | Log level (debug, info, warn, error) | "info" |
| `FARO_METRICS_PORT` | Metrics server port | "8080" |
| `FARO_CORRELATION_TIMEOUT` | Correlation completion timeout | "5m" |
| `FARO_INCLUDE_LOGS` | Include container logs in output | "true" |
| `FARO_MAX_LOG_STREAMS` | Maximum number of concurrent log streams | "50" |

## Output Format

### JSON Structure

```json
{
  "metadata": {
    "correlation_id": "deployment-uid-123",
    "start_time": "2025-08-07T10:00:00.000Z",
    "end_time": "2025-08-07T10:05:30.000Z",
    "event_count": 150,
    "namespace": "default",
    "resource_type": "apps/v1/Deployment",
    "resource_name": "my-app"
  },
  "events": [
    {
      "correlation_id": "deployment-uid-123",
      "timestamp": "2025-08-07T10:00:00.123Z",
      "event_type": "RESOURCE_UPDATE",
      "source_component": "my-app-deployment",
      "data": { /* Full Kubernetes object */ },
      "namespace": "default",
      "resource": "Deployment",
      "action": "add"
    }
  ]
}
```

### Event Types

- `RESOURCE_UPDATE`: Any Kubernetes resource changes (Pods, Deployments, Services, CRDs, etc.)
- `K8S_EVENT`: Kubernetes Events (scheduler, kubelet, controller messages)
- `CONTAINER_LOG`: Container log entries from pods
- `POD_LIFECYCLE`: Pod lifecycle changes (Pending, Running, Failed, etc.)
- `CORRELATED_EVENT`: Events related to the primary resource through owner references

## Development

### Project Structure

```
faro/
├── cmd/faro/             # Application entry point
├── internal/             # Core business logic
│   ├── agent.go         # Main agent coordinator
│   ├── config.go        # Configuration management
│   ├── models.go        # Data models and event types
│   ├── watcher.go       # Generic Kubernetes resource watcher
│   ├── correlation.go   # Event correlation engine
│   └── output.go        # JSON output writer
├── docs/                # Documentation
├── examples/            # Usage examples
└── scripts/             # Build and test scripts
```

### Building and Testing

```bash
# Run tests
make test

# Run tests with coverage
make test-coverage

# Run linting
make lint

# Build for different platforms
make build-linux
make build-darwin
```

### Universal Resource Watching

Faro uses a **single, generic watcher** that works with any Kubernetes resource type. This is possible because:

1. **Standardized Kubernetes API**: All Kubernetes objects follow the same structure with standard fields like `metadata`, `spec`, `status`, `apiVersion`, `kind`, etc.

2. **Dynamic Resource Discovery**: The watcher dynamically discovers resource types at runtime using the Kubernetes API's discovery mechanism.

3. **Generic Event Processing**: Events from any resource type are processed using the same unified event model.

**No Special Watchers Needed**: You don't need to write specific code for each resource type. The same watcher handles:
- Built-in resources (Pods, Deployments, Services, etc.)
- Custom resources (CRDs, Operators, etc.)
- Any future resource types

### Adding New Event Types

1. Add event type constant in `internal/models.go`
2. Update the generic watcher to emit new event types
3. Update correlation logic if needed
4. Add tests for new event types

### Supported Resource Types

Faro supports watching **any Kubernetes resource type** automatically, including:

**Built-in Resources:**
- `v1/Pod` - Individual pods
- `apps/v1/Deployment` - Deployments
- `apps/v1/ReplicaSet` - ReplicaSets
- `apps/v1/StatefulSet` - StatefulSets
- `apps/v1/DaemonSet` - DaemonSets
- `v1/Service` - Services
- `v1/ConfigMap` - ConfigMaps
- `v1/Secret` - Secrets
- `v1/Event` - Kubernetes Events
- And any other built-in resource

**Custom Resources (CRDs):**
- Any custom resource defined by `apiVersion` and `kind`
- Examples: `mycompany.com/v1/MyCustomResource`, `operators.coreos.com/v1/Subscription`
- Automatically discovered and watched without code changes

**Multiple Resources:**
- Use comma-separated values: `--resource="apps/v1/Deployment,v1/Service"`
- All resources will be watched simultaneously using the same generic watcher

## Monitoring and Observability

### Logging

Faro uses structured logging with correlation IDs:

```json
{
  "level": "info",
  "time": "2025-08-07T10:00:00.000Z",
  "correlation_id": "deployment-uid-123",
  "message": "Processing event",
  "event_type": "POD_UPDATE",
  "source": "my-app-pod-xyz"
}
```

### Metrics

Available metrics (exposed on `/metrics` endpoint):

- `faro_events_processed_total`: Total events processed
- `faro_correlations_active`: Number of active correlations
- `faro_worker_pool_size`: Current worker pool size
- `faro_log_streams_active`: Number of active log streams

### Health Checks

Health check endpoint at `/health`:

```json
{
  "status": "healthy",
  "timestamp": "2025-08-07T10:00:00.000Z",
  "uptime": "1h30m",
  "active_correlations": 5,
  "active_workers": 5
}
```

## Troubleshooting

### Common Issues

1. **Permission Denied**: Ensure proper RBAC permissions for watching resources
2. **High Memory Usage**: Reduce correlation timeout or increase worker count
3. **Missing Events**: Check namespace permissions and resource existence
4. **Log Stream Failures**: Verify pod access and container names

### Debug Mode

Enable debug logging:

```bash
FARO_LOG_LEVEL=debug ./bin/faro --cr-type="apps/v1/Deployment"
```

### Performance Tuning

- **High Event Volume**: Increase worker count and buffer size
- **Memory Issues**: Reduce correlation timeout
- **Network Issues**: Configure proper kubeconfig and connection timeouts

## Contributing

### Development Setup

1. Fork the repository
2. Create a feature branch
3. Make changes with tests
4. Run full test suite
5. Submit pull request

### Code Style

- Follow Go conventions and `gofmt`
- Use meaningful variable and function names
- Add comments for complex logic
- Write tests for new functionality

### Testing

- Unit tests for all packages
- Integration tests for watchers
- End-to-end tests for complete workflows
- Performance benchmarks for critical paths

## License

[License information to be added]

## Support

- **Issues**: Report bugs and feature requests via GitHub issues
- **Documentation**: See `docs/` directory for detailed documentation
- **Examples**: Check `examples/` directory for usage examples

---

**Project Faro** - Capturing the complete story of any Kubernetes resource lifecycle. 