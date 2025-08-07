# High-Level Design: Kubernetes Resource Analyzer

**Version:** 1.0
**Date:** August 7, 2025
**Codename:** Project Faro

## 1. Vision & Goals

### 1.1. Problem Statement

Debugging complex, transient, or performance-related issues in Kubernetes resources is challenging because:

- Resource lifecycle involves cascading events across multiple related resources (Pods, Events, Services, CRDs, etc.)
- Logs are scattered across different components and namespaces
- It's difficult to reconstruct the exact sequence of events after the fact
- Understanding *why* a resource's lifecycle was slow, failed, or behaved unexpectedly requires correlating multiple data sources
- Multi-tenant environments require data isolation and filtering
- Complex microservices span multiple namespaces
- Pods get deleted and recreated during deployments, losing critical log data
- Traditional monitoring tools don't capture the complete story across namespaces and resource types

### 1.2. Vision

To create a system that captures a high-fidelity, chronological record of all relevant events and state changes during any Kubernetes resource's lifecycle across multiple namespaces. This captured "resource story" will be compiled into a structured JSON file, suitable for offline debugging and for submission to AI/ML services for analysis.

**Faro can watch absolutely any Kubernetes resource** - from built-in resources like Pods, Deployments, and Services, to custom resources (CRDs) like Operators, Controllers, or any custom workload. Whether you're debugging a simple Pod deployment or a complex multi-resource Operator, Faro captures the complete story.

---

## 2. System Architecture

The system is composed of a single primary component: the **Collector Agent**. It is an external application that connects to the Kubernetes cluster and captures high-fidelity, chronological records of any Kubernetes resource's lifecycle across multiple namespaces.

```text
┌─────────────────────────────────────────────────────────────┐
│                    External Environment                     │
│              (CI Runner, Developer Machine)                 │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐    │
│  │              Collector Agent (Go App)               │    │
│  │  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐    │    │
│  │  │ Universal   │ │Dynamic      │ │ Correlation │    │    │
│  │  │ Resource    │ │Worker Pool  │ │   Engine    │    │    │
│  │  │ Watcher     │ │             │ │             │    │    │
│  │  └─────────────┘ └─────────────┘ └─────────────┘    │    │
│  │  ┌─────────────┐ ┌─────────────┐ ┌──────────────┐   │    │
│  │  │ Multi-NS    │ │ Regex       │ │ Pod Lifecycle│   │    │
│  │  │ Manager     │ │ Filters     │ │ Manager      │   │    │
│  │  └─────────────┘ └─────────────┘ └──────────────┘   │    │
│  └─────────────────────────────────────────────────────┘    │
│                           │                                 │
└───────────────────────────│─────────────────────────────── ─┘
                            │ (Kubeconfig API Connection)
                            ▼
┌─────────────────────────────────────────────────────────────┐
│              Kubernetes Cluster API Server                  │
│                                                             │
│  Watches:                                                   │
│  ├── Any Kubernetes Resource                                │
│  │   ├── Built-in Resources (Pods, Deployments, etc.)       │
│  │   ├── Custom Resources (CRDs, Operators, etc.)           │
│  │   └── Events (v1.Event)                                  │
│  └── Container Logs (Real-Time Streams)                     │
└─────────────────────────────────────────────────────────────┘
```

### 2.1. Core Components

1. **Universal Resource Watcher**: A single, generic watcher that works with any Kubernetes resource type
2. **Multi-Namespace Manager**: Handles watching resources across multiple namespaces simultaneously
3. **Regex Filters**: Advanced filtering for multi-tenant environments and noise reduction
4. **Pod Lifecycle Manager**: Captures logs from pods that get deleted and recreated
5. **Dynamic Worker Pool**: Automatically scales workers based on actual load without artificial limits
6. **Correlation Engine**: Groups related events by correlation ID across namespaces
7. **Streaming Components**: Real-time streaming of container logs and Kubernetes events

---

## 3. Data Collection Strategy

The **Collector Agent** is a standalone Go application that connects to a target Kubernetes cluster via its API server. It uses the `client-go` library to establish multiple, simultaneous, namespace-scoped watches on any Kubernetes resource type.

### 3.1. Universal Resource Watching

The agent uses a **single, generic watcher** that works with any Kubernetes resource type. This is possible because:

1. **Standardized Kubernetes API**: All Kubernetes objects follow the same structure with standard fields like `metadata`, `spec`, `status`, `apiVersion`, `kind`, etc.

2. **Dynamic Resource Discovery**: The watcher dynamically discovers resource types at runtime using the Kubernetes API's discovery mechanism.

3. **Generic Event Processing**: Events from any resource type are processed using the same unified event model.

**No Special Watchers Needed**: You don't need to write specific code for each resource type. The same watcher handles:
- Built-in resources (Deployments, Services, ConfigMaps, etc.)
- Custom resources (CRDs, Operators, etc.)
- Any future resource types

### 3.2. Multi-Namespace Support

The agent supports watching resources across multiple namespaces simultaneously:

- **Namespace-Specific Configurations**: Different resources can be watched in different namespaces
- **Cross-Namespace Correlation**: Events from multiple namespaces can be correlated
- **Pattern Matching**: Support for namespace patterns (e.g., `prod-*`, `staging-*`)
- **All Namespaces**: Support for watching all namespaces (`--namespace="all"`)

### 3.3. Real-Time Streaming Components

The agent provides **real-time streaming** of two critical data sources:

#### **Container Log Streaming**
- **Automatic Detection**: When containers start running, Faro automatically begins streaming their logs
- **Real-Time Capture**: Logs are captured as they're generated, not just at resource change events
- **Multi-Container Support**: Streams logs from all containers (main app, sidecars, init containers)
- **Configurable Limits**: Control the number of concurrent log streams
- **Correlation Integration**: Log entries are correlated with resource lifecycle events

#### **Event Streaming**
- **Live Event Capture**: Events are streamed as they occur, not just when resources change
- **Narrative Context**: Events provide human-readable explanations of what's happening
- **Scheduler Messages**: Real-time scheduler decisions and placement information
- **Controller Actions**: Live controller reconciliation activities
- **Error Narratives**: Immediate error explanations and troubleshooting context

### 3.4. Regex Filtering for Multi-Tenant Support

The agent supports **regex-based filtering** for multi-tenant environments:

- **Pod Log Filtering**: Regex patterns for pod name filtering
- **Event Filtering**: Regex patterns for event source filtering
- **Container Filtering**: Focus on specific containers (app, main, etc.)
- **Log Content Filtering**: Filter log lines by content patterns
- **Exclusion Patterns**: Filter out shared infrastructure

### 3.5. Pod Lifecycle Management

The agent intelligently handles pod lifecycle transitions where pods get deleted and recreated:

- **Deletion Detection**: Automatically detects when pods are being deleted
- **Log Preservation**: Captures logs during graceful shutdown
- **Recreation Tracking**: Correlates logs from old and new pods
- **Transition Handling**: Maintains correlation across pod recreations
- **Crash Log Capture**: Preserves logs from failed pods

### 3.6. Dynamic Scaling

The agent uses **dynamic, self-scaling architecture** with no artificial limits:

- **Universal Resource Watching**: Watch any number of resources without limits
- **Adaptive Worker Pool**: Automatically scales based on actual load
- **Self-Discovery**: Dynamically finds and watches resources
- **No Configuration Limits**: System handles scaling automatically

### 3.7. Asynchronous Processing

To ensure the agent is non-blocking and can handle a high volume of events, it uses a dynamic worker pool pattern:

* Universal resource watchers do minimal work: they simply package the received object into a unified struct and place it onto a buffered Go channel.

* A dynamic pool of worker goroutines reads from this channel and buffers the events in memory, grouped by their `correlation_id`.

* The system automatically scales workers based on actual load without artificial limits.

---

## 4. Data Output

### 4.1. Unified Data Model

All captured data points, regardless of their source, are structured into a standardized JSON object. This ensures consistency for analysis.

**Key Fields:**

* `correlation_id`: A unique identifier for the entire resource lifecycle flow, typically the UID of the primary resource.

* `timestamp`: An ISO 8601 timestamp with millisecond precision.

* `event_type`: A string indicating the source (e.g., `RESOURCE_UPDATE`, `K8S_EVENT`, `CONTAINER_LOG`, `POD_TERMINATING`, `POD_STARTING`).

* `source_component`: The name of the Kubernetes object that emitted the event.

* `namespace`: The namespace where the event occurred.

* `data`: The full JSON payload of the event, log line, or resource state.

### 4.2. Output Format

Upon the termination of a capture (see section 5.3), the Collector Agent compiles all buffered events for a given `correlation_id` into a single document. The output is a JSON file where the root object contains metadata and an `events` key, which is an array of all captured event objects, sorted chronologically.

### 4.3. Cross-Namespace Correlation

The system supports correlation across multiple namespaces:

- **Multi-Namespace Metadata**: Output includes all relevant namespaces
- **Cross-Namespace Events**: Events from different namespaces are correlated
- **Unified Timeline**: Chronological order maintained across namespaces
- **Namespace Context**: Each event includes namespace information

### 4.4. Pod Lifecycle Events

The output includes special events for pod lifecycle management:

- **POD_TERMINATING**: Pod is being deleted (with preserved logs)
- **POD_STARTING**: New pod is starting (correlated with previous)
- **CONTAINER_LOG**: Container log entries with correlation
- **CORRELATED_EVENT**: Events related through owner references

---

## 5. Data Correlation Strategy

The `correlation_id` is the cornerstone of this design, supporting complex correlation scenarios.

### 5.1. Universal Resource Correlation

1. **Initiation:** When a new resource is detected, its UID is designated as the `correlation_id` for a new "resource story." All subsequent related events are buffered under this ID.

2. **Propagation:** The Collector analyzes the `involvedObject` reference in Kubernetes Events and the `ownerReferences` on resources. If these references trace back to the primary resource, the event is tagged with the corresponding `correlation_id`.

3. **Cross-Namespace Correlation:** Events from multiple namespaces are correlated when they share the same logical resource lifecycle.

### 5.2. Multi-Tenant Correlation

- **Tenant Isolation:** Different tenants' events are isolated through regex filtering
- **Shared Infrastructure:** Common infrastructure events can be filtered out
- **Tenant-Specific Correlation:** Each tenant's events are correlated independently

### 5.3. Pod Lifecycle Correlation

- **Deletion/Recreation:** Logs from deleted pods are preserved and correlated with new pods
- **Transition Tracking:** Pod lifecycle transitions are tracked and correlated
- **Crash Analysis:** Failed pod logs are preserved for debugging

### 5.4. Termination

The capture for a specific `correlation_id` concludes when:
- The primary resource reaches a terminal state
- A timeout period has elapsed
- The system detects completion based on resource state

At this point, the agent writes the final JSON file with the complete correlation story.

This ensures that all disparate data streams related to a single logical operation can be retrieved and reconstructed into a coherent, chronological timeline for analysis, regardless of namespace boundaries or resource types.
