# Go-based Agent Principles for Project Faro

**Version:** 1.0  
**Date:** August 7, 2025  

---

## 1. Universal Resource Watching Architecture

The agent will be built on a foundation of **universal resource watching** with no artificial limits. A **single, generic watcher** will handle any Kubernetes resource type using the standardized Kubernetes API. Go **channels** will serve as the primary mechanism for decoupling event sources (Kubernetes API watches, log streams, event streams) from the processing and buffering logic. A dedicated **goroutine** will handle each watcher and stream, feeding events into a shared, buffered channel. This approach prevents backpressure and ensures the agent can capture a high volume of events in real time across any resource type.

**Key Principles:**
- **No Special Watchers**: Single generic watcher for all resource types
- **Dynamic Resource Discovery**: Runtime discovery of resource types
- **Standardized API**: Leverage Kubernetes API consistency
- **Universal Support**: Built-in resources, CRDs, and future resource types

---

## 2. Multi-Namespace and Multi-Tenant Support

The agent will support **multi-namespace monitoring** with **namespace-specific configurations** and **regex filtering** for multi-tenant environments. The system will handle complex scenarios where different namespaces have different resource types to watch, and where multiple tenants share the same namespace.

**Key Principles:**
- **Namespace-Specific Configurations**: Different resources per namespace
- **Cross-Namespace Correlation**: Events correlated across namespaces
- **Regex Filtering**: Multi-tenant isolation and noise reduction
- **Pattern Matching**: Support for namespace patterns (`prod-*`, `staging-*`)
- **All Namespaces**: Support for watching all namespaces (`--namespace="all"`)

---

## 3. Real-Time Streaming Components

The agent will provide **real-time streaming** of two critical data sources that are essential for complete workload analysis:

### **Container Log Streaming**
- **Automatic Detection**: When containers start running, automatically begin streaming logs
- **Real-Time Capture**: Logs captured as they're generated, not just at resource changes
- **Multi-Container Support**: Streams from all containers (main app, sidecars, init containers)
- **Configurable Limits**: Control concurrent log streams
- **Correlation Integration**: Log entries correlated with resource lifecycle events

### **Event Streaming**
- **Live Event Capture**: Events streamed as they occur
- **Narrative Context**: Human-readable explanations of what's happening
- **Scheduler Messages**: Real-time scheduler decisions and placement
- **Controller Actions**: Live controller reconciliation activities
- **Error Narratives**: Immediate error explanations and troubleshooting

---

## 4. Pod Lifecycle Management

The agent will intelligently handle **pod lifecycle transitions** where pods get deleted and recreated during deployments, scaling, or failures. This is crucial for capturing the complete story without losing critical log data.

**Key Principles:**
- **Deletion Detection**: Automatically detect when pods are being deleted
- **Log Preservation**: Capture logs during graceful shutdown
- **Recreation Tracking**: Correlate logs from old and new pods
- **Transition Handling**: Maintain correlation across pod recreations
- **Crash Log Capture**: Preserve logs from failed pods
- **Correlation Continuity**: Ensure no gaps in log history

---

## 5. Dynamic Scaling Without Artificial Limits

The agent will use **dynamic, self-scaling architecture** with no artificial limits. The system will automatically scale workers based on actual load rather than predefined calculations.

**Key Principles:**
- **No Predefined Limits**: No fixed worker counts or resource limits
- **Load-Based Scaling**: Scale based on actual CPU, memory, I/O usage
- **Self-Discovery**: Dynamically find and watch resources
- **Adaptive Worker Pool**: Automatically scale workers up/down
- **Resource Efficiency**: Only use resources when actually needed
- **Performance Monitoring**: Real-time metrics for scaling decisions

---

## 6. Structured Concurrency and Worker Pools

The agent will leverage Go's concurrency primitives to manage tasks safely and efficiently. A **dynamic worker pool** of goroutines will read from the main event channel, perform event correlation, and buffer the data in memory. This pattern ensures that CPU-intensive correlation logic doesn't block event ingestion. The use of a `Context` will be essential for managing the lifecycle of these goroutines, allowing for graceful shutdowns of watches and streams when the capture is complete.

**Key Principles:**
- **Dynamic Worker Allocation**: Scale workers based on actual load
- **Context-Based Lifecycle**: Graceful shutdown of all components
- **Non-Blocking Operations**: Prevent backpressure during high event volumes
- **Memory Management**: Efficient event buffering with configurable limits
- **Cross-Namespace Processing**: Handle events from multiple namespaces

---

## 7. Comprehensive Observability

The agent will be observable, providing clear insight into its operations without requiring deep access to its internal state. A structured logging framework will be used to emit detailed logs with correlation IDs. The logging will include periodic **`info` logs** that summarize runtime statistics, such as the number of events processed per minute, the number of active correlations, current memory usage, and cross-namespace correlation metrics.

**Key Principles:**
- **Structured Logging**: Detailed logs with correlation IDs
- **Runtime Statistics**: Events per minute, active correlations, memory usage
- **Health Monitoring**: Built-in health checks and metrics
- **Cross-Namespace Metrics**: Monitor multi-namespace operations
- **Stream Performance**: Monitor log and event stream performance
- **Filter Effectiveness**: Track regex filter performance

---

## 8. Resilient and Idempotent Data Handling

The agent's data processing must be resilient to transient failures. Events will be buffered in memory, and the final output will be a single, complete JSON file. The process of writing this file upon a correlation's termination will be **atomic**. While this design means that in-flight data may be lost in the event of a crash, it prioritizes a clean, complete output, and avoids the complexity of persistent storage for temporary data.

**Key Principles:**
- **Atomic File Writing**: Complete, consistent JSON output
- **Memory Buffering**: Efficient in-memory event storage
- **Correlation Integrity**: Maintain correlation across failures
- **Pod Lifecycle Resilience**: Preserve logs during transitions
- **Multi-Namespace Resilience**: Handle namespace-specific failures

---

## 9. Clear Separation of Concerns

Each component of the agent will have a specific, well-defined role. The universal resource watcher will be solely responsible for watching any Kubernetes resource and pushing events to a channel. The worker pool will be dedicated to event correlation and buffering. The regex filters will handle multi-tenant isolation. The pod lifecycle manager will handle pod transitions. A separate component will handle the final serialization and writing of the JSON file to disk. This separation makes the codebase easier to reason about, test, and maintain over time.

**Key Principles:**
- **Universal Resource Watcher**: Single component for all resource types
- **Multi-Namespace Manager**: Handle namespace-specific configurations
- **Regex Filter Engine**: Multi-tenant isolation and noise reduction
- **Pod Lifecycle Manager**: Handle pod deletion/recreation
- **Dynamic Worker Pool**: Scale based on actual load
- **Correlation Engine**: Group events by correlation ID
- **Output Writer**: Atomic JSON file generation

---

## 10. Performance and Scalability

The agent will be designed for high performance and scalability across complex, multi-namespace, multi-tenant environments.

**Key Principles:**
- **No Artificial Limits**: Scale based on actual system capabilities
- **Efficient Resource Usage**: Only use resources when needed
- **Streaming Performance**: Real-time log and event streaming
- **Filter Performance**: Optimized regex filtering
- **Memory Management**: Efficient buffering and correlation storage
- **Network Efficiency**: Optimized API connections and data transfer
- **Graceful Degradation**: Handle high load without data loss

---

## 11. Configuration and Flexibility

The agent will support flexible configuration for complex environments while maintaining simplicity for basic use cases.

**Key Principles:**
- **Simple Command Line**: Basic usage with minimal configuration
- **YAML Configuration**: Complex multi-namespace setups
- **Environment Variables**: Deployment flexibility
- **Dynamic Discovery**: Auto-discover resources and namespaces
- **Regex Flexibility**: Advanced filtering capabilities
- **Namespace Patterns**: Support for pattern matching
- **No Hard Limits**: Configuration should not impose artificial constraints

This architecture ensures that Faro can handle any Kubernetes environment, from simple single-namespace deployments to complex multi-tenant, multi-namespace microservices architectures, while maintaining high performance, reliability, and observability.