![banner](./faro.jpeg)

<div align="center">

[![Go Reference](https://pkg.go.dev/badge/github.com/T0MASD/faro.svg)](https://pkg.go.dev/github.com/T0MASD/faro)
[![License](https://img.shields.io/badge/License-Unlicense-blue.svg)](https://unlicense.org/)
[![Go Version](https://img.shields.io/github/go-mod/go-version/T0MASD/faro)](https://go.dev/)

</div>

# Faro - Kubernetes Resource Monitoring

**Clean, flexible Kubernetes resource monitoring** - Use as a **Go library** or deploy as an **operator**.

> 📚 **Library-First Design**: Pure mechanisms for Kubernetes resource monitoring  
> 🎯 **Two Deployment Modes**: Embed in your Go applications or deploy as a standalone operator  
> 🔧 **Clean Architecture**: Provides tools and mechanisms, not policies  
> 🚀 **Production Ready**: Comprehensive testing, secure RBAC, Prometheus metrics

---

## Quick Start

### As a Go Library

```bash
go get github.com/T0MASD/faro
```

```go
package main

import (
    "log"
    faro "github.com/T0MASD/faro/pkg"
)

func main() {
    config, _ := faro.LoadConfig()
    client, _ := faro.NewKubernetesClient()
    logger, _ := faro.NewLogger(config)
    controller := faro.NewController(client, logger, config)
    
    controller.AddEventHandler(&MyHandler{})
    controller.Start()
}

type MyHandler struct{}

func (h *MyHandler) OnMatched(event faro.MatchedEvent) error {
    log.Printf("Event: %s %s/%s", event.EventType, event.Object.GetNamespace(), event.Object.GetName())
    return nil
}
```

### As a Kubernetes Operator

```bash
# Deploy operator
kubectl apply -k deploy/operator/

# Verify deployment
kubectl get pods -n faro-system

# View metrics
kubectl port-forward -n faro-system svc/faro-operator-metrics 8080:8080
curl http://localhost:8080/metrics
```

**Container Images:**
- `ghcr.io/t0masd/faro-operator:latest` - Latest stable release
- `ghcr.io/t0masd/faro-operator:v1.0.0` - Specific version

---

## Why Faro?

| **Feature** | **Faro** | **kubectl watch** | **Custom Controllers** |
|-------------|----------|-------------------|------------------------|
| **Server-side Filtering** | ✅ Labels + field selectors | ❌ Basic only | ⚠️ Manual |
| **JSON Event Export** | ✅ Structured output | ❌ Text only | ⚠️ Custom |
| **Readiness Callbacks** | ✅ Built-in | ❌ No signal | ⚠️ Manual |
| **Graceful Shutdown** | ✅ Clean cleanup | ❌ Kill only | ⚠️ Manual |
| **Dynamic Informers** | ✅ Runtime creation | ❌ Static | ✅ Full control |
| **Prometheus Metrics** | ✅ Built-in | ❌ None | ⚠️ Manual |
| **Operator Deployment** | ✅ Ready-to-use | ❌ N/A | ⚠️ Build yourself |
| **Library Integration** | ✅ Import and use | ❌ CLI only | ✅ Full control |

---

## Features

### Core Capabilities

- 🎯 **Dual Deployment**: Use as Go library or Kubernetes operator
- 🔍 **Server-side Filtering**: Efficient label selectors and field matching
- 📊 **Prometheus Metrics**: Built-in observability (events, informers, health)
- 📝 **JSON Event Export**: Structured event output for integration
- 🔄 **Dynamic Resource Discovery**: Add/remove resources at runtime
- 🛡️ **Secure RBAC**: Read-only access, no secrets, principle of least privilege
- ⚡ **Graceful Lifecycle**: Clean startup, readiness signals, shutdown handling
- 🧪 **Comprehensive Testing**: Unit, E2E, integration, and operator deployment tests

### Operator Features

When deployed as an operator, Faro provides:

- **In-cluster Authentication**: Automatic ServiceAccount token handling
- **Health & Readiness Probes**: Kubernetes-native health checks via `/metrics`
- **Resource Limits**: Production-ready CPU/memory constraints
- **Security Hardening**: Minimal capabilities, non-root user, read-only filesystem
- **ConfigMap-driven**: Easy configuration updates without redeployment
- **Event Persistence**: JSON event files stored in persistent volumes

---

## Architecture

### Philosophy: Mechanisms, Not Policies

**Faro Core Provides (Mechanisms):**
- ✅ Informer management and lifecycle
- ✅ Event streaming with work queues
- ✅ Server-side filtering (labels, fields, namespaces)
- ✅ JSON export and structured logging
- ✅ Prometheus metrics and health endpoints
- ✅ Graceful startup, readiness, and shutdown

**Users Implement (Policies):**
- 🔧 Business logic and event processing
- 🔧 Complex filtering and correlation
- 🔧 Workload detection and CRD discovery
- 🔧 External integrations and workflows
- 🔧 Custom actions and automation

### Component Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        Faro Controller                       │
├─────────────────────────────────────────────────────────────┤
│  Config Loader  │  K8s Client  │  Logger  │  Metrics Server │
├─────────────────────────────────────────────────────────────┤
│              Multi-layered Informer Manager                  │
│  ┌────────────────┐  ┌────────────────┐  ┌───────────────┐ │
│  │ Config-driven  │  │   Dynamic      │  │  Namespace    │ │
│  │   Informers    │  │   Informers    │  │   Scoped      │ │
│  └────────────────┘  └────────────────┘  └───────────────┘ │
├─────────────────────────────────────────────────────────────┤
│                      Event Processing                        │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐  │
│  │ Work Queues  │→ │Event Handlers│→ │JSON Export/Logs │  │
│  └──────────────┘  └──────────────┘  └──────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

---

## Configuration

Faro supports two configuration formats: **namespace-centric** and **resource-centric**.

### Namespace-Centric Configuration

Monitor multiple resources within specific namespaces:

```yaml
output_dir: "/var/faro/events"
log_level: "info"
auto_shutdown_sec: 0  # Run indefinitely (operator mode)
json_export: true

metrics:
  enabled: true
  port: 8080
  path: "/metrics"
  bind_addr: "0.0.0.0"

namespaces:
  - name_selector: "production"
    resources:
      "v1/pods":
        label_selector: "app=nginx"
      "v1/services": {}
      "apps/v1/deployments": {}
      "batch/v1/jobs": {}
```

### Resource-Centric Configuration

Monitor specific resources across multiple namespaces:

```yaml
output_dir: "./logs"
log_level: "info"
json_export: true

metrics:
  enabled: true
  port: 8080

resources:
  - gvr: "v1/configmaps"
    namespace_names: ["default", "kube-system"]
    label_selector: "app=web"
  - gvr: "batch/v1/cronjobs"
    namespace_names: ["production"]
    field_selector: "metadata.name=backup-job"
```

### Configuration Options

| Field | Type | Description |
|-------|------|-------------|
| `output_dir` | string | Directory for logs and JSON exports |
| `log_level` | string | `debug`, `info`, `warning`, `error`, `fatal` |
| `auto_shutdown_sec` | int | Auto-shutdown after N seconds (0 = disabled) |
| `json_export` | bool | Enable structured JSON event export |
| `metrics.enabled` | bool | Enable Prometheus metrics server |
| `metrics.port` | int | Metrics server port (default: 8080) |

---

## Library Usage

### Basic Integration

```go
package main

import (
    "log"
    faro "github.com/T0MASD/faro/pkg"
)

func main() {
    // Load configuration
    config, err := faro.LoadConfig()
    if err != nil {
        log.Fatal(err)
    }

    // Create Kubernetes client (auto-detects in-cluster or kubeconfig)
    client, err := faro.NewKubernetesClient()
    if err != nil {
        log.Fatal(err)
    }

    // Create logger
    logger, err := faro.NewLogger(config)
    if err != nil {
        log.Fatal(err)
    }

    // Create controller
    controller := faro.NewController(client, logger, config)

    // Register event handler
    controller.AddEventHandler(&MyEventHandler{})

    // Set readiness callback
    controller.SetReadyCallback(func() {
        log.Println("Faro is ready!")
    })

    // Start controller
    controller.Start()
}

type MyEventHandler struct{}

func (h *MyEventHandler) OnMatched(event faro.MatchedEvent) error {
    log.Printf("Event: %s %s %s/%s",
        event.EventType,
        event.GVR,
        event.Object.GetNamespace(),
        event.Object.GetName())
    return nil
}
```

### Advanced Usage: Dynamic Resource Discovery

```go
type DynamicDiscovery struct {
    controller *faro.Controller
}

func (d *DynamicDiscovery) OnMatched(event faro.MatchedEvent) error {
    // Watch for new CRDs and dynamically add them
    if event.GVR == "apiextensions.k8s.io/v1/customresourcedefinitions" {
        if event.EventType == "ADDED" {
            gvr := extractGVRFromCRD(event.Object)
            d.controller.AddResources([]faro.ResourceConfig{{
                GVR: gvr,
                NamespaceNames: []string{"default"},
            }})
            d.controller.StartInformers()
        }
    }
    return nil
}
```

### API Reference

```go
// Controller creation
controller := faro.NewController(client, logger, config)

// Event handlers (implement your business logic)
controller.AddEventHandler(handler EventHandler)

// JSON middleware (modify objects before export)
controller.AddJSONMiddleware(middleware JSONMiddleware)

// Readiness callback (initialization complete)
controller.SetReadyCallback(func() {
    fmt.Println("Ready!")
})

// Check readiness
if controller.IsReady() {
    // All informers synced
}

// Dynamic resource management
controller.AddResources([]faro.ResourceConfig{...})
controller.StartInformers()

// Get active informer counts
configCount, dynamicCount := controller.GetActiveInformers()

// Lifecycle management
controller.Start()  // Blocks until shutdown
controller.Stop()   // Graceful shutdown
```

---

## Operator Deployment

### Prerequisites

- Kubernetes cluster (1.24+)
- `kubectl` configured

### Installation

#### Option 1: Using Kustomize (Recommended)

```bash
# Deploy operator with default configuration
kubectl apply -k deploy/operator/

# Verify deployment
kubectl get pods -n faro-system
kubectl logs -n faro-system -l app.kubernetes.io/name=faro-operator -f

# Check metrics
kubectl port-forward -n faro-system svc/faro-operator-metrics 8080:8080
curl http://localhost:8080/metrics
```

#### Option 2: Using Scripts

```bash
# Deploy operator
bash scripts/deploy-operator.sh

# Cleanup
bash scripts/cleanup-operator.sh
```

### Configuration

Modify the ConfigMap to customize monitoring:

```bash
kubectl edit configmap -n faro-system faro-operator-config
```

Example configuration:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: faro-operator-config
  namespace: faro-system
data:
  config.yaml: |
    output_dir: "/var/faro/events"
    log_level: "info"
    auto_shutdown_sec: 0
    json_export: true
    
    metrics:
      enabled: true
      port: 8080
      path: "/metrics"
    
    namespaces:
      - name_selector: "production"
        resources:
          "v1/pods": {}
          "v1/services": {}
          "apps/v1/deployments": {}
```

### Accessing Events

```bash
# Get operator pod name
POD=$(kubectl get pod -n faro-system -l app.kubernetes.io/name=faro-operator -o jsonpath='{.items[0].metadata.name}')

# View captured events
kubectl exec -n faro-system $POD -- ls -lh /var/faro/events/
kubectl exec -n faro-system $POD -- cat /var/faro/events/events-*.json
```

### Monitoring

The operator exposes Prometheus metrics:

```bash
# Port-forward metrics endpoint
kubectl port-forward -n faro-system svc/faro-operator-metrics 8080:8080

# View metrics
curl http://localhost:8080/metrics
```

**Key Metrics:**
- `faro_events_total` - Total events processed by GVR and type
- `faro_informer_health` - Informer health status
- `faro_gvr_per_informer` - Resources tracked per informer
- `faro_informer_last_event_timestamp` - Last event timestamp per informer

### Security

The operator is deployed with security best practices:

- **Read-only RBAC**: Can only `get`, `list`, `watch` resources
- **No secrets access**: Explicitly denied
- **Resource limits**: CPU and memory constraints
- **Security context**: Non-root user, dropped capabilities
- **Health probes**: Kubernetes-native health checks

View RBAC permissions:

```bash
# Check what operator can do
kubectl auth can-i list pods --as=system:serviceaccount:faro-system:faro-operator
kubectl auth can-i get secrets --as=system:serviceaccount:faro-system:faro-operator  # Should be "no"
```

---

## Testing

Faro includes comprehensive test coverage:

### Unit Tests (No Kubernetes Required)

```bash
make test-unit
```

Tests configuration parsing, logger functionality, and core logic.

### E2E Tests (Requires Kubernetes)

```bash
make test-e2e
```

Tests core library functionality with real Kubernetes clusters.

### Integration Tests (Requires Kubernetes)

```bash
make test-integration
```

Tests library user implementations (dynamic discovery, workload monitoring).

### Operator Tests (Requires Kubernetes)

```bash
make test-operator
```

End-to-end validation of operator deployment:
- Image building
- RBAC configuration
- Metrics endpoint
- Event capture
- Security restrictions

### All Tests

```bash
make test  # Runs unit + e2e + integration tests
```

### Local Development with kinc

Faro includes scripts for local Kubernetes testing:

```bash
# Start local kinc cluster
podman run -d --name kinc-cluster \
  --hostname kinc-control-plane \
  --cap-add=SYS_ADMIN \
  -p 127.0.0.1:6443:6443/tcp \
  ghcr.io/t0masd/kinc:latest

# Extract kubeconfig
mkdir -p ~/.kube
podman cp kinc-cluster:/etc/kubernetes/admin.conf ~/.kube/config
sed -i 's|server: https://.*:6443|server: https://127.0.0.1:6443|g' ~/.kube/config

# Run tests
make test
```

---

## CI/CD

Faro uses GitHub Actions for continuous integration:

### CI Workflow

On every push and pull request:
- ✅ Unit tests
- ✅ E2E tests (parallel job with kinc)
- ✅ Integration tests (parallel job with kinc)
- ✅ Operator deployment tests (parallel job with kinc)
- ✅ Library import validation
- ✅ Test artifact uploads

### Release Workflow

On version tags (`v*`):
- ✅ Full test suite validation
- ✅ GoReleaser library release
- ✅ **Container image build and push** to `ghcr.io/t0masd/faro-operator`
- ✅ Multi-tag support (`latest`, `v1.0.0`, `v1.0`, `v1`)

**View CI Status:** https://github.com/T0MASD/faro/actions

---

## JSON Event Export

When `json_export: true`, events are exported in structured format:

```json
{
  "timestamp": "2025-11-10T14:33:02Z",
  "eventType": "ADDED",
  "gvr": "v1/pods",
  "namespace": "default",
  "name": "nginx-abc123",
  "uid": "12345678-1234-1234-1234-123456789012",
  "resourceVersion": "12345",
  "labels": {
    "app": "nginx",
    "version": "1.0"
  },
  "annotations": {
    "kubectl.kubernetes.io/last-applied-configuration": "..."
  }
}
```

Events are written to:
- **Library mode**: `${output_dir}/events-YYYYMMDD-HHMMSS.json`
- **Operator mode**: `/var/faro/events/events-YYYYMMDD-HHMMSS.json`

---

## Examples

Check the `examples/` directory for real-world usage:

- **`library-usage.go`** - Basic library integration
- **`workload-monitor.go`** - Dynamic workload detection
- **`worker-dispatcher.go`** - Event processing and actions

---

## Documentation

- [Architecture Overview](docs/architecture.md) - Design principles and patterns
- [Component Reference](docs/components/) - Detailed component documentation
  - [Controller](docs/components/controller.md) - Informer management
  - [Client](docs/components/client.md) - Kubernetes client
  - [Config](docs/components/config.md) - Configuration formats
  - [Logger](docs/components/logger.md) - Structured logging
  - [Metrics](docs/metrics.md) - Prometheus metrics
- [Examples](examples/) - Real-world implementations

---

## Development

### Building from Source

```bash
# Build library binary
make build

# Build with version info
make build-dev

# Build operator image
make operator-image

# Load image into local cluster
make operator-image-load
```

### Project Structure

```
faro/
├── pkg/                    # Core library code
│   ├── client.go          # Kubernetes client
│   ├── config.go          # Configuration loading
│   ├── controller.go      # Main controller
│   ├── logger.go          # Structured logging
│   └── metrics.go         # Prometheus metrics
├── main.go                # CLI entrypoint
├── Dockerfile             # Operator container image
├── deploy/operator/       # Kubernetes manifests
│   ├── namespace.yaml
│   ├── serviceaccount.yaml
│   ├── clusterrole.yaml
│   ├── clusterrolebinding.yaml
│   ├── configmap.yaml
│   ├── deployment.yaml
│   ├── service.yaml
│   └── kustomization.yaml
├── scripts/               # Deployment and test scripts
│   ├── deploy-operator.sh
│   ├── cleanup-operator.sh
│   └── test-operator.sh
├── tests/                 # Test suites
│   ├── unit/
│   ├── e2e/
│   ├── integration/
│   └── operator-ci/
├── examples/              # Usage examples
└── docs/                  # Documentation

```

### Makefile Targets

```bash
make help          # Show all available targets
make build         # Build faro binary
make test          # Run all tests
make test-unit     # Run unit tests
make test-e2e      # Run E2E tests
make test-integration  # Run integration tests
make test-operator     # Run operator deployment tests
make clean         # Clean build artifacts
make operator-image    # Build operator container image
```

---

## Contributing

Contributions are welcome! Please feel free to submit issues or pull requests.

### Running Tests Locally

1. Ensure you have a Kubernetes cluster available (kinc recommended for local development)
2. Run `make test` to validate your changes
3. Run `make test-operator` to validate operator deployment

---

## License

This project is released into the public domain under the [Unlicense](https://unlicense.org/).

---

## Key Principles

1. **Library provides mechanisms** - Informers, events, JSON export, metrics
2. **Users implement policies** - Business logic, filtering, workflows
3. **No fallbacks or defaults** - Errors are surfaced, not hidden
4. **Clean separation** - Core library vs. application concerns
5. **Dual deployment** - Use as library or operator, your choice

**Faro gives you the tools. You build the solutions.**

---

<div align="center">

Made with ❤️ for the Kubernetes community

</div>
