# Faro Examples

This directory contains examples demonstrating how to use Faro following the **clean architecture principle**: Faro provides **mechanisms**, users implement **policies**.

## 🏗️ Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                    LIBRARY USERS (Policies)                 │
├─────────────────────────────────────────────────────────────┤
│ • Business Logic & Workflows                               │
│ • CRD Discovery & Management                                │
│ • Event-driven GVR Discovery                               │
│ • Workload Detection & Annotation                          │
│ • Complex Configuration Interpretation                     │
│ • External System Integration                              │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼ (Event Callbacks)
┌─────────────────────────────────────────────────────────────┐
│                   FARO CORE (Mechanisms)                    │
├─────────────────────────────────────────────────────────────┤
│ • Informer Management (Create, Start, Stop)                │
│ • Event Streaming (Work Queues, Rate Limiting)             │
│ • Server-side Filtering (Label/Field Selectors)            │
│ • JSON Export (Structured Output)                          │
│ • Lifecycle Management (Graceful Shutdown)                 │
└─────────────────────────────────────────────────────────────┘
```

## 📚 Examples

### ✅ **Compliant Examples** (Follow Clean Architecture)

#### 1. `library-usage.go` - Basic Library Integration
**Demonstrates**: Simple event handling with clean separation of concerns
- **Faro Core**: Provides informer management and event streaming
- **User Code**: Implements event handlers with business logic
- **Architecture**: ✅ Perfect separation of mechanisms vs policies

```bash
go run library-usage.go
```

#### 2. `worker-dispatcher.go` - Resource-Specific Workers
**Demonstrates**: Advanced event processing with specialized workers
- **Faro Core**: Provides event streaming mechanisms
- **User Code**: Implements worker dispatch pattern and resource-specific logic
- **Architecture**: ✅ Excellent example of policy implementation

```bash
go run worker-dispatcher.go
```

#### 3. `workload-monitor.go` - Dynamic Workload Detection
**Demonstrates**: Complex business logic properly separated from Faro core with dynamic configuration
- **Faro Core**: Provides informer management, event streaming, JSON export
- **User Code**: Implements workload detection, dynamic GVR discovery, business workflows
- **Architecture**: ✅ Proper implementation of mechanisms vs policies
- **Dynamic**: Configured via command-line parameters, not hardcoded

```bash
# Example usage with dynamic configuration
go run workload-monitor.go \
  -discover-namespaces="app.kubernetes.io/name~faro" \
  -extract-from-namespace="env-staging-(.+)" \
  -namespace-resources="v1/configmaps,batch/v1/jobs,v1/events" \
  -cluster-resources="v1/namespaces" \
  -log-level="info"
```

### ⚠️ **Legacy Examples** (Architectural Issues)

#### 4. `workload-monitor-old.go` - Legacy Implementation
**Issues**: Mixes Faro mechanisms with complex business logic
- ❌ Implements CRD discovery in example code
- ❌ Contains workload detection that should be user responsibility  
- ❌ Mixes policies with mechanisms

**Status**: Kept for compatibility, but **use `workload-monitor.go` instead**

## 🔧 Configuration

### `config-with-metrics.yaml` - Metrics Configuration
Simple configuration demonstrating Faro's basic filtering mechanisms:
- **Faro Core**: Provides basic resource filtering and metrics collection
- **User Code**: Implements complex business logic for discovered resources

## 🎯 Key Principles Demonstrated

### **Faro Core Provides (Mechanisms):**
- ✅ **Informer Management**: Create, start, stop Kubernetes informers
- ✅ **Event Streaming**: Reliable event delivery with work queues  
- ✅ **Server-side Filtering**: Efficient API-level resource filtering
- ✅ **JSON Export**: Structured event output for integration
- ✅ **Lifecycle Management**: Graceful startup, readiness, shutdown

### **Library Users Implement (Policies):**
- 🔧 **Business Logic**: CRD discovery, workload detection, annotation processing
- 🔧 **Configuration Interpretation**: Complex selectors, patterns, rules
- 🔧 **Event Processing**: Filtering, correlation, actions, workflows  
- 🔧 **Integration Logic**: External systems, notifications, automation

## 🚀 Getting Started

1. **Start Simple**: Begin with `library-usage.go` to understand basic concepts
2. **Add Complexity**: Move to `worker-dispatcher.go` for advanced patterns
3. **Business Logic**: Study `workload-monitor.go` for complex use cases
4. **Avoid**: Don't use `workload-monitor-old.go` as a reference (architectural issues)

## 📖 Further Reading

- [Architecture Documentation](../docs/architecture.md)
- [Controller Component](../docs/components/controller.md)
- [Configuration Guide](../docs/components/config.md)

## 🔍 Example Comparison

| Example | Mechanisms (Faro Core) | Policies (User Code) | Architecture |
|---------|------------------------|---------------------|--------------|
| `library-usage.go` | Informer management, event streaming | Simple event handlers | ✅ Clean |
| `worker-dispatcher.go` | Event streaming, JSON export | Worker dispatch, resource logic | ✅ Clean |
| `workload-monitor.go` | Informer management, streaming, JSON | Workload detection, business logic | ✅ Clean |
| `workload-monitor-old.go` | Mixed with business logic | Mixed with mechanisms | ❌ Legacy |

Choose examples that demonstrate proper separation of concerns for your use case.