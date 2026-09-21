# Faro Examples

This directory contains examples demonstrating how to use Faro following the **clean architecture principle**: Faro provides **mechanisms**, users implement **policies**.

## 🏗️ Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                    LIBRARY USERS (Policies)                 │
├─────────────────────────────────────────────────────────────┤
│ • Business Logic & Workflows                                │
│ • CRD Discovery & Management                                │
│ • Event-driven GVR Discovery                                │
│ • Workload Detection & Annotation                           │
│ • Complex Configuration Interpretation                      │
│ • External System Integration                               │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼ (Event Callbacks)
┌─────────────────────────────────────────────────────────────┐
│                   FARO CORE (Mechanisms)                    │
├─────────────────────────────────────────────────────────────┤
│ • Informer Management (Create, Start, Stop)                 │
│ • Event Streaming (Work Queues, Rate Limiting)              │
│ • Server-side Filtering (Label/Field Selectors)             │
│ • JSON Export (Structured Output)                           │
│ • Lifecycle Management (Graceful Shutdown)                  │
└─────────────────────────────────────────────────────────────┘
```

## 📚 Examples

### ✅ **Compliant Examples** (Follow Clean Architecture)

#### 1. `library-usage/` - Basic Library Integration
**Demonstrates**: Event handling, and hearing whether each informer listed
- **Faro Core**: Provides informer management, event streaming, informer status
- **User Code**: Implements event handlers and an informer status handler
- **Architecture**: ✅ Perfect separation of mechanisms vs policies

An `InformerStatusHandler` reports what a watch did. An empty result, a refusal,
and a type the endpoint does not serve all deliver no events, so `OnMatched`
cannot tell them apart.

```bash
go run ./examples/library-usage
```

#### 2. `worker-dispatcher/` - Resource-Specific Workers
**Demonstrates**: Advanced event processing with specialized workers
- **Faro Core**: Provides event streaming mechanisms
- **User Code**: Implements worker dispatch pattern and resource-specific logic
- **Architecture**: ✅ Excellent example of policy implementation

```bash
go run ./examples/worker-dispatcher
```

#### 3. `workload-monitor/` - Dynamic Workload Detection
**Demonstrates**: Complex business logic properly separated from Faro core with dynamic configuration
- **Faro Core**: Provides informer management, event streaming, JSON export
- **User Code**: Implements workload detection, dynamic GVR discovery, business workflows
- **Architecture**: ✅ Proper implementation of mechanisms vs policies
- **Dynamic**: Configured via command-line parameters, not hardcoded

```bash
# Example usage with dynamic configuration
go run ./examples/workload-monitor \
  -discover-namespaces="app.kubernetes.io/name~faro" \
  -extract-from-namespace="env-staging-(.+)" \
  -namespace-resources="v1/configmaps,batch/v1/jobs,v1/events" \
  -cluster-resources="v1/namespaces" \
  -log-level="info"
```

## 🔧 Configuration

### `config-with-metrics.yaml` - Metrics Configuration
Simple configuration demonstrating Faro's basic filtering mechanisms:
- **Faro Core**: Provides basic resource filtering and metrics collection
- **User Code**: Implements complex business logic for discovered resources

## 🎯 Key Principles Demonstrated

### **Faro Core Provides (Mechanisms):**
- ✅ **Informer Management**: Create, start, stop Kubernetes informers
- ✅ **Event Streaming**: Reliable event delivery with work queues  
- ✅ **Server-side Filtering**: Exact names and label selectors, evaluated by the API server
- ✅ **JSON Export**: Structured event output for integration
- ✅ **Lifecycle Management**: Graceful startup, readiness, shutdown

### **Library Users Implement (Policies):**
- 🔧 **Business Logic**: CRD discovery, workload detection, annotation processing
- 🔧 **Configuration Interpretation**: Complex selectors, patterns, rules
- 🔧 **Event Processing**: Filtering, correlation, actions, workflows  
- 🔧 **Integration Logic**: External systems, notifications, automation

## 🚀 Getting Started

1. **Start Simple**: Begin with `library-usage/` to understand basic concepts
2. **Add Complexity**: Move to `worker-dispatcher/` for advanced patterns
3. **Business Logic**: Study `workload-monitor/` for complex use cases

Each example is its own package under `examples/`, so `go build ./...` covers them
and `go run ./examples/<name>` runs one.

## 📖 Further Reading

- [Architecture Documentation](../docs/architecture.md)
- [Controller Component](../docs/components/controller.md)
- [Configuration Guide](../docs/components/config.md)

## 🔍 Example Comparison

| Example | Mechanisms (Faro Core) | Policies (User Code) | Architecture |
|---------|------------------------|---------------------|--------------|
| `library-usage/` | Informer management, event streaming, informer status | Event handlers, status handler | ✅ Clean |
| `worker-dispatcher/` | Event streaming, JSON export | Worker dispatch, resource logic | ✅ Clean |
| `workload-monitor/` | Informer management, streaming, JSON | Workload detection, business logic | ✅ Clean |

Choose examples that demonstrate proper separation of concerns for your use case.