package faro

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// MetricsCollector manages Prometheus metrics for Faro
type MetricsCollector struct {
	enabled       bool
	server        *http.Server
	registry      *prometheus.Registry
	logger        Logger
	mu            sync.RWMutex
	
	// Core metrics
	informerCount         *prometheus.GaugeVec
	gvrPerInformer        *prometheus.GaugeVec
	eventsPerGVR          *prometheus.CounterVec
	informerSyncDuration  *prometheus.HistogramVec
	trackedResources      *prometheus.GaugeVec
	uidResolutionSuccess  *prometheus.CounterVec
	
	// Advanced metrics
	cacheHitRate          *prometheus.GaugeVec
	informerLastEventTime *prometheus.GaugeVec
	informerHealth        *prometheus.GaugeVec
	
	// Internal tracking
	startTime             time.Time
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector(config MetricsConfig, logger Logger) *MetricsCollector {
	if !config.Enabled {
		return &MetricsCollector{enabled: false, logger: logger}
	}
	
	// Set defaults
	if config.Port == 0 {
		config.Port = 8080
	}
	if config.Path == "" {
		config.Path = "/metrics"
	}
	if config.BindAddr == "" {
		config.BindAddr = "0.0.0.0"
	}
	
	registry := prometheus.NewRegistry()
	
	mc := &MetricsCollector{
		enabled:   true,
		registry:  registry,
		logger:    logger,
		startTime: time.Now(),
	}
	
	mc.initializeMetrics()
	mc.startServer(config)
	
	return mc
}

// initializeMetrics creates all Prometheus metrics
func (mc *MetricsCollector) initializeMetrics() {
	// Core informer metrics
	mc.informerCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "faro_informers_total",
			Help: "Total number of active informers by status",
		},
		[]string{"status"}, // active, syncing, failed
	)
	
	mc.gvrPerInformer = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "faro_gvr_per_informer",
			Help: "Number of GVRs tracked per informer",
		},
		[]string{"gvr", "namespace_scoped"},
	)
	
	mc.eventsPerGVR = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "faro_events_total",
			Help: "Total number of events processed per GVR and type",
		},
		[]string{"gvr", "event_type"}, // Removed namespace to reduce cardinality
	)
	
	mc.informerSyncDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "faro_informer_sync_duration_seconds",
			Help:    "Time taken for informer initial sync completion",
			Buckets: []float64{0.1, 0.5, 1.0, 2.0, 5.0, 10.0, 30.0, 60.0},
		},
		[]string{"gvr"},
	)
	
	mc.trackedResources = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "faro_tracked_resources_total",
			Help: "Number of resources currently tracked in UID cache per GVR",
		},
		[]string{"gvr"}, // Removed namespace to reduce cardinality - aggregate by GVR only
	)
	
	mc.uidResolutionSuccess = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "faro_uid_resolution_total",
			Help: "UID resolution attempts and outcomes",
		},
		[]string{"gvr", "status"}, // success, unknown, cache_miss
	)
	
	// Advanced metrics
	mc.cacheHitRate = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "faro_cache_hit_rate",
			Help: "UID cache hit rate per GVR (0.0 to 1.0)",
		},
		[]string{"gvr"},
	)
	
	mc.informerLastEventTime = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "faro_informer_last_event_timestamp",
			Help: "Unix timestamp of last event processed by informer",
		},
		[]string{"gvr"},
	)
	
	mc.informerHealth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "faro_informer_health",
			Help: "Informer health status (1=healthy, 0=unhealthy)",
		},
		[]string{"gvr", "status"}, // healthy, sync_failed, stale_events - limited enum values
	)
	
	// Register all metrics
	mc.registry.MustRegister(
		mc.informerCount,
		mc.gvrPerInformer,
		mc.eventsPerGVR,
		mc.informerSyncDuration,
		mc.trackedResources,
		mc.uidResolutionSuccess,
		mc.cacheHitRate,
		mc.informerLastEventTime,
		mc.informerHealth,
	)
	
	// Add standard Go metrics
	mc.registry.MustRegister(prometheus.NewGoCollector())
	mc.registry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
}

// startServer starts the HTTP metrics server
func (mc *MetricsCollector) startServer(config MetricsConfig) {
	mux := http.NewServeMux()
	mux.Handle(config.Path, promhttp.HandlerFor(mc.registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/health", mc.healthHandler)
	mux.HandleFunc("/ready", mc.readinessHandler)
	
	addr := fmt.Sprintf("%s:%d", config.BindAddr, config.Port)
	mc.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	
	go func() {
		mc.logger.Info("metrics", fmt.Sprintf("Starting metrics server on %s%s", addr, config.Path))
		if err := mc.server.ListenAndServe(); err != http.ErrServerClosed {
			mc.logger.Error("metrics", fmt.Sprintf("Metrics server error: %v", err))
		}
	}()
}

// Health and readiness handlers
func (mc *MetricsCollector) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (mc *MetricsCollector) readinessHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Ready"))
}

// Shutdown gracefully shuts down the metrics server
func (mc *MetricsCollector) Shutdown(ctx context.Context) error {
	if !mc.enabled || mc.server == nil {
		return nil
	}
	
	mc.logger.Info("metrics", "Shutting down metrics server...")
	return mc.server.Shutdown(ctx)
}

// === INFORMER LIFECYCLE HOOKS ===

// OnInformerCreated is called when a new informer is created
func (mc *MetricsCollector) OnInformerCreated(gvr string, scope apiextensionsv1.ResourceScope) {
	if !mc.enabled {
		return
	}
	
	mc.informerCount.WithLabelValues("syncing").Inc()
	mc.gvrPerInformer.WithLabelValues(gvr, strconv.FormatBool(scope == apiextensionsv1.NamespaceScoped)).Set(1)
	mc.informerHealth.WithLabelValues(gvr, "healthy").Set(1) // Use controlled enum value
}

// OnInformerSyncCompleted is called when informer initial sync completes
func (mc *MetricsCollector) OnInformerSyncCompleted(gvr string, syncDuration time.Duration, resourceCount int64) {
	if !mc.enabled {
		return
	}
	
	mc.informerCount.WithLabelValues("syncing").Dec()
	mc.informerCount.WithLabelValues("active").Inc()
	mc.informerSyncDuration.WithLabelValues(gvr).Observe(syncDuration.Seconds())
	mc.informerHealth.WithLabelValues(gvr, "healthy").Set(1) // Use controlled enum value
	
	mc.logger.Debug("metrics", fmt.Sprintf("Informer %s synced in %v with %d resources", gvr, syncDuration, resourceCount))
}

// OnInformerSyncFailed is called when informer sync fails
func (mc *MetricsCollector) OnInformerSyncFailed(gvr string, err error) {
	if !mc.enabled {
		return
	}
	
	mc.informerCount.WithLabelValues("syncing").Dec()
	mc.informerCount.WithLabelValues("failed").Inc()
	mc.informerHealth.WithLabelValues(gvr, "sync_failed").Set(0) // Controlled enum value
	
	mc.logger.Error("metrics", fmt.Sprintf("Informer %s sync failed: %v", gvr, err))
}

// === EVENT PROCESSING HOOKS ===

// OnEventProcessed is called when an event is processed
func (mc *MetricsCollector) OnEventProcessed(gvr, eventType, namespace string) {
	if !mc.enabled {
		return
	}
	
	mc.eventsPerGVR.WithLabelValues(gvr, eventType).Inc() // Removed namespace parameter
	mc.informerLastEventTime.WithLabelValues(gvr).Set(float64(time.Now().Unix()))
}

// OnResourceTracked is called when a resource is added to UID cache
func (mc *MetricsCollector) OnResourceTracked(gvr, namespace string, delta int64) {
	if !mc.enabled {
		return
	}
	
	// Aggregate by GVR only to reduce cardinality
	if delta > 0 {
		mc.trackedResources.WithLabelValues(gvr).Add(float64(delta))
	} else {
		mc.trackedResources.WithLabelValues(gvr).Sub(float64(-delta))
	}
}

// OnUIDResolution is called when UID resolution is attempted
func (mc *MetricsCollector) OnUIDResolution(gvr, status string) {
	if !mc.enabled {
		return
	}
	
	mc.uidResolutionSuccess.WithLabelValues(gvr, status).Inc()
}


// UpdateCacheHitRate updates the cache hit rate for a GVR
func (mc *MetricsCollector) UpdateCacheHitRate(gvr string, hitRate float64) {
	if !mc.enabled {
		return
	}
	
	mc.cacheHitRate.WithLabelValues(gvr).Set(hitRate)
}

// SetInformerStale marks an informer as having stale events
func (mc *MetricsCollector) SetInformerStale(gvr string, isStale bool) {
	if !mc.enabled {
		return
	}
	
	if isStale {
		mc.informerHealth.WithLabelValues(gvr, "stale_events").Set(0)
	} else {
		mc.informerHealth.WithLabelValues(gvr, "healthy").Set(1)
	}
}

// === UTILITY METHODS ===

// IsEnabled returns whether metrics collection is enabled
func (mc *MetricsCollector) IsEnabled() bool {
	return mc.enabled
}

// GetUptime returns the uptime of the metrics collector
func (mc *MetricsCollector) GetUptime() time.Duration {
	return time.Since(mc.startTime)
}

// ResetMetrics resets all metrics (useful for testing)
func (mc *MetricsCollector) ResetMetrics() {
	if !mc.enabled {
		return
	}
	
	mc.mu.Lock()
	defer mc.mu.Unlock()
	
	// Reset all metrics to zero
	mc.informerCount.Reset()
	mc.gvrPerInformer.Reset()
	mc.eventsPerGVR.Reset()
	mc.trackedResources.Reset()
	mc.uidResolutionSuccess.Reset()
	mc.cacheHitRate.Reset()
	mc.informerLastEventTime.Reset()
	mc.informerHealth.Reset()
}