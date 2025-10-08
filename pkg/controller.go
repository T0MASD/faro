package faro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// ResourceInfo holds information about a discovered API resource
type ResourceInfo struct {
	Group      string
	Version    string
	Resource   string
	Kind       string
	Namespaced bool
}

// WorkItem represents a queued object key and associated metadata for processing
type WorkItem struct {
	Key         string             // Object key (namespace/name or name)
	GVRString   string             // Group/Version/Resource identifier
	Configs     []NormalizedConfig // Configuration rules that apply to this GVR
	EventType   string             // ADDED, UPDATED, DELETED
	// For DELETED events - preserve metadata that's lost when object is removed from cache
	DeletedUID         string            // UID of deleted object
	DeletedAnnotations map[string]string // Annotations of deleted object
}

// MatchedEvent represents a filtered event that matched configuration criteria
type MatchedEvent struct {
	EventType string                      // ADDED, UPDATED, DELETED
	Object    *unstructured.Unstructured  // Full Kubernetes object
	GVR       string                      // Group/Version/Resource identifier
	Key       string                      // namespace/name or name
	Config    NormalizedConfig            // Configuration that matched this event
	Timestamp time.Time                   // When the event was processed
}

// JSONEvent represents a structured JSON event for export
type JSONEvent struct {
	Timestamp   string            `json:"timestamp"`
	EventType   string            `json:"eventType"`
	GVR         string            `json:"gvr"`
	Namespace   string            `json:"namespace,omitempty"`
	Name        string            `json:"name"`
	UID         string            `json:"uid,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	
	// Additional fields can be added by library users via middleware
}

// EventHandler interface for handling matched events via callbacks
type EventHandler interface {
	OnMatched(event MatchedEvent) error
}

// JSONMiddleware interface for processing objects before JSON logging
type JSONMiddleware interface {
	// ProcessBeforeJSON is called before JSON logging to allow modification of the object
	// Returns the modified object and whether to continue processing
	ProcessBeforeJSON(eventType, gvr, namespace, name, uid string, obj *unstructured.Unstructured) (*unstructured.Unstructured, bool)
}

// InformerStateTracker tracks UID state for a specific GVR using informer lifecycle
type InformerStateTracker struct {
	GVR           string
	Lister        cache.GenericLister
	UIDCache      sync.Map // map[resourceKey]string (UID)
	SyncCompleted bool
	mu            sync.RWMutex
}




// logJSONEvent creates and logs a structured JSON event with middleware support
func (c *Controller) logJSONEvent(eventType, gvr, namespace, name, uid string, labels map[string]string, obj *unstructured.Unstructured) {
	var objCopy *unstructured.Unstructured
	var annotations map[string]string
	var timestamp string
	var finalUID string = uid

	// Handle DELETED events - try to get UID from informer state
	if eventType == "DELETED" {
		// For DELETED events, try to get UID from informer state if not provided or unknown
		if uid == "" || uid == "unknown" {
			finalUID = c.getUIDFromInformerState(gvr, namespace, name)
		}
	}
	
	// Create object copy for middleware processing
	if obj != nil {
		// RACE CONDITION FIX: Create a deep copy to avoid concurrent map access
		objCopy = obj.DeepCopy()
		
		
		annotations = objCopy.GetAnnotations()
		timestamp = objCopy.GetCreationTimestamp().UTC().Format(time.RFC3339Nano)
	} else {
		// For DELETED events, create a minimal object for middleware processing
		objCopy = &unstructured.Unstructured{}
		objCopy.SetName(name)
		if namespace != "" {
			objCopy.SetNamespace(namespace)
		}
		if finalUID != "" && finalUID != "unknown" {
			objCopy.SetUID(types.UID(finalUID))
		}
		if annotations != nil {
			objCopy.SetAnnotations(annotations)
		}
		timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}

	// Apply JSON middleware to modify object before logging
	c.middlewareMu.RLock()
	middleware := c.jsonMiddleware
	c.middlewareMu.RUnlock()
	
	processedObj := objCopy
	shouldContinue := true
	
	for _, mw := range middleware {
		if !shouldContinue {
			break
		}
		processedObj, shouldContinue = mw.ProcessBeforeJSON(eventType, gvr, namespace, name, finalUID, processedObj)
	}
	
	// Skip logging if middleware says not to continue
	if !shouldContinue {
		return
	}
	
	// Update annotations and labels from processed object
	if processedObj != nil {
		annotations = processedObj.GetAnnotations()
		labels = processedObj.GetLabels()
	}
	
	jsonEvent := JSONEvent{
		Timestamp:   timestamp,
		EventType:   eventType,
		GVR:         gvr,
		Namespace:   namespace,
		Name:        name,
		UID:         finalUID,
		Labels:      labels,
		Annotations: annotations,
	}

	// Special field extraction removed - library users should implement via middleware if needed

	jsonData, err := json.Marshal(jsonEvent)
	if err != nil {
		c.logger.Warning("controller", fmt.Sprintf("Failed to marshal JSON event: %v", err))
		return
	}

	// Log as JSON for the JSONFileHandler to pick up
	c.logger.Debug("controller", string(jsonData))
}


// getUIDFromInformerState retrieves UID from informer state tracker
func (c *Controller) getUIDFromInformerState(gvrString, namespace, name string) string {
	trackerInterface, exists := c.informerTrackers.Load(gvrString)
	if !exists {
		c.metrics.OnUIDResolution(gvrString, "cache_miss")
		return "unknown" // No tracker for this GVR
	}
	
	tracker := trackerInterface.(*InformerStateTracker)
	key := c.makeResourceKey(gvrString, namespace, name)
	
	if cachedUID, exists := tracker.UIDCache.Load(key); exists {
		c.metrics.OnUIDResolution(gvrString, "success")
		return cachedUID.(string)
	}
	
	c.metrics.OnUIDResolution(gvrString, "unknown")
	return "unknown" // Not found in informer state
}

// cleanupUIDFromInformerState removes UID from informer state tracker after processing
func (c *Controller) cleanupUIDFromInformerState(gvrString, namespace, name string) {
	trackerInterface, exists := c.informerTrackers.Load(gvrString)
	if !exists {
		return // No tracker for this GVR
	}
	
	tracker := trackerInterface.(*InformerStateTracker)
	key := c.makeResourceKey(gvrString, namespace, name)
	tracker.UIDCache.Delete(key)
}



// makeResourceKey creates a consistent key for resource tracking
func (c *Controller) makeResourceKey(gvr, namespace, name string) string {
	if namespace == "" {
		return gvr + "/" + name // Cluster-scoped resource
	}
	return gvr + "/" + namespace + "/" + name
}

// copyStringMap creates a deep copy of a string map
func (c *Controller) copyStringMap(original map[string]string) map[string]string {
	if original == nil {
		return nil
	}
	copy := make(map[string]string, len(original))
	for k, v := range original {
		copy[k] = v
	}
	return copy
}

// populateInitialUIDCache populates the UID cache with existing resources from informer
func (c *Controller) populateInitialUIDCache(tracker *InformerStateTracker, config InformerConfig) int64 {
	var objects []runtime.Object
	var err error
	var resourceCount int64
	
	// List all resources from the informer's lister
	objects, err = tracker.Lister.List(labels.Everything())
	
	if err != nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to list resources for %s: %v", config.GVRString, err))
		return 0
	}
	
	for _, obj := range objects {
		if unstructured, ok := obj.(*unstructured.Unstructured); ok {
			key := c.makeResourceKey(config.GVRString, unstructured.GetNamespace(), unstructured.GetName())
			uid := string(unstructured.GetUID())
			tracker.UIDCache.Store(key, uid)
			resourceCount++
			
			// Update metrics for tracked resource
			c.metrics.OnResourceTracked(config.GVRString, unstructured.GetNamespace(), 1)
			
			c.logger.Debug("controller", fmt.Sprintf("Cached existing resource: %s (UID: %s)", key, uid))
		}
	}
	
	return resourceCount
}

// setupSyncCallback sets up callback-driven sync detection and cache population
func (c *Controller) setupSyncCallback(informer cache.SharedIndexInformer, tracker *InformerStateTracker, config InformerConfig) {
	syncStartTime := time.Now()
	
	// Use sync.Once to ensure sync logic only runs once
	var syncOnce sync.Once
	
	// Add a special event handler that detects when informer is synced
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			// Check if this is the first event after sync
			if informer.HasSynced() {
				syncOnce.Do(func() {
					// Sync completed - populate cache
					resourceCount := c.populateInitialUIDCache(tracker, config)
					
					tracker.mu.Lock()
					tracker.SyncCompleted = true
					tracker.mu.Unlock()
					
					syncDuration := time.Since(syncStartTime)
					c.metrics.OnInformerSyncCompleted(config.GVRString, syncDuration, resourceCount)
					
					c.logger.Info("controller", "Initial UID cache populated for "+config.GVRString+" with "+fmt.Sprintf("%d", resourceCount)+" resources in "+syncDuration.String())
				})
			}
		},
	})
}

// createStateTrackingEventHandlers creates event handlers that maintain UID state
func (c *Controller) createStateTrackingEventHandlers(tracker *InformerStateTracker, config InformerConfig) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if unstructured, ok := obj.(*unstructured.Unstructured); ok {
				// Update UID cache
				key := c.makeResourceKey(config.GVRString, unstructured.GetNamespace(), unstructured.GetName())
				uid := string(unstructured.GetUID())
				tracker.UIDCache.Store(key, uid)
				
				// Update metrics
				c.metrics.OnEventProcessed(config.GVRString, "ADDED", unstructured.GetNamespace())
				c.metrics.OnResourceTracked(config.GVRString, unstructured.GetNamespace(), 1)
				
				// Call original handler
				config.HandlerFunc("ADDED", unstructured)
			} else {
				c.logger.Error("controller", fmt.Sprintf("Received unexpected object type in AddFunc for %s", config.GVRString))
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if unstructured, ok := newObj.(*unstructured.Unstructured); ok {
				// Update UID cache (UID shouldn't change, but keep it current)
				key := c.makeResourceKey(config.GVRString, unstructured.GetNamespace(), unstructured.GetName())
				uid := string(unstructured.GetUID())
				tracker.UIDCache.Store(key, uid)
				
				// Update metrics
				c.metrics.OnEventProcessed(config.GVRString, "UPDATED", unstructured.GetNamespace())
				
				// Call original handler
				config.HandlerFunc("UPDATED", unstructured)
			} else {
				c.logger.Error("controller", fmt.Sprintf("Received unexpected object type in UpdateFunc for %s", config.GVRString))
			}
		},
		DeleteFunc: func(obj interface{}) {
			var unstructuredObj *unstructured.Unstructured
			var ok bool
			
			// Handle tombstone
			if tombstone, isTombstone := obj.(cache.DeletedFinalStateUnknown); isTombstone {
				unstructuredObj, ok = tombstone.Obj.(*unstructured.Unstructured)
				if !ok {
					c.logger.Error("controller", fmt.Sprintf("Tombstone contained unexpected object type for %s", config.GVRString))
					return
				}
			} else {
				unstructuredObj, ok = obj.(*unstructured.Unstructured)
				if !ok {
					c.logger.Error("controller", fmt.Sprintf("Received unexpected object type in DeleteFunc for %s", config.GVRString))
					return
				}
			}
			
			if ok {
				key := c.makeResourceKey(config.GVRString, unstructuredObj.GetNamespace(), unstructuredObj.GetName())
				
				// Get UID from cache before deletion (for logging) - no fallbacks
				cachedUID, exists := tracker.UIDCache.Load(key)
				if !exists {
					c.logger.Error("controller", "No cached UID for DELETED event: "+key)
					return
				}
				uid := cachedUID.(string)
				
				// Update metrics
				c.metrics.OnEventProcessed(config.GVRString, "DELETED", unstructuredObj.GetNamespace())
				c.metrics.OnResourceTracked(config.GVRString, unstructuredObj.GetNamespace(), -1)
				
				// DON'T remove from cache yet - let the work queue processing handle cleanup
				// This ensures the UID is available when the work queue processes the DELETED event
				
				// Create enhanced unstructured object with UID for handler
				enhancedObj := unstructuredObj.DeepCopy()
				if uid != "" {
					enhancedObj.SetUID(types.UID(uid))
				}
				
				// Call original handler with UID-enhanced object
				config.HandlerFunc("DELETED", enhancedObj)
			}
		},
	}
}


// InformerConfig holds configuration for creating a generic informer
type InformerConfig struct {
	GVR         schema.GroupVersionResource
	Scope       apiextensionsv1.ResourceScope
	GVRString   string
	Context     context.Context
	HandlerFunc func(eventType string, obj *unstructured.Unstructured)
	Name        string // For logging purposes
}

// Controller implements the sophisticated multi-layered informer architecture
type Controller struct {
	client *KubernetesClient
	logger *Logger
	config *Config

	// Context management
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Work queue for processing events asynchronously
	workQueue workqueue.RateLimitingInterface
	workers   int // Number of worker goroutines

	// API discovery results
	discoveredResources   map[string]*ResourceInfo // map[GVR] -> ResourceInfo
	discoveredResourcesMu sync.RWMutex             // Protects discoveredResources map

	// Informer lifecycle management - using GVR string as consistent key
	cancellers      sync.Map // map[string]context.CancelFunc for informer shutdown
	activeInformers sync.Map // map[string]bool for tracking active informers by GVR
	listers         sync.Map // map[string]cache.GenericLister for object retrieval


	// Event handlers for library usage
	eventHandlers []EventHandler
	handlersMu    sync.RWMutex

	// JSON middleware for processing objects before JSON logging
	jsonMiddleware []JSONMiddleware
	middlewareMu   sync.RWMutex

	// Informer state tracking for UID preservation
	informerTrackers sync.Map // map[string]*InformerStateTracker for UID tracking per GVR
	
	// Metrics collection
	metrics *MetricsCollector

	// Readiness callback
	onReady   func()
	readyMu   sync.Mutex
	isReady   bool
}

// NewController creates an informer-based controller
func NewController(client *KubernetesClient, logger *Logger, config *Config) *Controller {
	ctx, cancel := context.WithCancel(context.Background())

	controller := &Controller{
		client:              client,
		logger:              logger,
		config:              config,
		ctx:                 ctx,
		cancel:              cancel,
		workQueue:           workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "faro-controller"),
		workers:             3, // Start with 3 worker goroutines
		discoveredResources: make(map[string]*ResourceInfo),
		eventHandlers:       make([]EventHandler, 0),
		jsonMiddleware:      make([]JSONMiddleware, 0),
		metrics:             NewMetricsCollector(config.Metrics, *logger),
	}
	
	logger.Debug("controller", "Created new controller instance")
	return controller
}

// AddEventHandler registers an event handler for matched events
func (c *Controller) AddEventHandler(handler EventHandler) {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	c.eventHandlers = append(c.eventHandlers, handler)
	c.logger.Debug("controller", fmt.Sprintf("Added event handler (total: %d)", len(c.eventHandlers)))
}

// AddJSONMiddleware registers a JSON middleware for processing objects before JSON logging
func (c *Controller) AddJSONMiddleware(middleware JSONMiddleware) {
	c.middlewareMu.Lock()
	defer c.middlewareMu.Unlock()
	c.jsonMiddleware = append(c.jsonMiddleware, middleware)
	c.logger.Debug("controller", fmt.Sprintf("Added JSON middleware (total: %d)", len(c.jsonMiddleware)))
}


// SetReadyCallback sets a callback function to be called when Faro is fully initialized and ready
func (c *Controller) SetReadyCallback(callback func()) {
	c.readyMu.Lock()
	defer c.readyMu.Unlock()
	c.onReady = callback
	
	// If already ready, call immediately
	if c.isReady && callback != nil {
		go callback()
	}
}

// IsReady returns true if Faro is fully initialized and ready to process events
func (c *Controller) IsReady() bool {
	c.readyMu.Lock()
	defer c.readyMu.Unlock()
	c.logger.Debug("controller", fmt.Sprintf("Readiness check: %t", c.isReady))
	return c.isReady
}

// AddResources dynamically adds new resource configurations to the controller
func (c *Controller) AddResources(newResources []ResourceConfig) {
	c.config.Resources = append(c.config.Resources, newResources...)
	c.logger.Info("controller", fmt.Sprintf("Added %d new resource configurations", len(newResources)))
}

// StartInformers starts informers for configured GVRs
func (c *Controller) StartInformers() error {
	c.logger.Info("controller", "Starting informers for configured GVRs")
	return c.startConfigDrivenInformers()
}



// createEventHandlers creates consistent event handlers with error checking
func (c *Controller) createEventHandlers(handlerFunc func(string, *unstructured.Unstructured), gvrString string) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if unstructuredObj, ok := obj.(*unstructured.Unstructured); ok {
				handlerFunc("ADDED", unstructuredObj)
			} else {
				c.logger.Error("controller", fmt.Sprintf("Received unexpected object type in AddFunc for %s", gvrString))
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if unstructuredObj, ok := newObj.(*unstructured.Unstructured); ok {
				handlerFunc("UPDATED", unstructuredObj)
			} else {
				c.logger.Error("controller", fmt.Sprintf("Received unexpected object type in UpdateFunc for %s", gvrString))
			}
		},
		DeleteFunc: func(obj interface{}) {
			var unstructuredObj *unstructured.Unstructured
			var ok bool
			
			// Handle Kubernetes cache tombstone objects properly
			if tombstone, isTombstone := obj.(cache.DeletedFinalStateUnknown); isTombstone {
				unstructuredObj, ok = tombstone.Obj.(*unstructured.Unstructured)
				if !ok {
					c.logger.Error("controller", fmt.Sprintf("Tombstone contained unexpected object type for %s", gvrString))
					return
				}
			} else {
				unstructuredObj, ok = obj.(*unstructured.Unstructured)
				if !ok {
					c.logger.Error("controller", fmt.Sprintf("Received unexpected object type in DeleteFunc for %s", gvrString))
					return
				}
			}
			
			handlerFunc("DELETED", unstructuredObj)
		},
	}
}

// runInformerWithLogging runs an informer with consistent logging
func (c *Controller) runInformerWithLogging(informer cache.SharedIndexInformer, ctx context.Context, description string) {
	c.logger.Info("controller", fmt.Sprintf("Starting %s", description))
	c.logger.Info("controller", fmt.Sprintf("Running %s", description))
	informer.Run(ctx.Done())
	c.logger.Info("controller", fmt.Sprintf("Stopped %s", description))
}



// Start initializes and starts the multi-layered informer architecture
func (c *Controller) Start() error {
	c.logger.Info("controller", "Starting sophisticated multi-layered informer controller")

	// Start worker goroutines for processing work queue
	for i := 0; i < c.workers; i++ {
		c.wg.Add(1)
		go c.runWorker()
	}

	// 1. Discover all available API resources in the cluster
	if err := c.discoverAPIResources(); err != nil {
		return fmt.Errorf("failed to discover API resources: %w", err)
	}

	// 2. Start informers based on configuration and discovery results
	c.logger.Info("controller", "Starting informers for configured GVRs")
	if err := c.startConfigDrivenInformers(); err != nil {
		return fmt.Errorf("failed to start informers: %w", err)
	}

	// CRD watching removed - library users should implement CRD discovery if needed

	c.logger.Info("controller", "Multi-layered informer architecture started successfully")
	
	// Trigger readiness callback
	c.readyMu.Lock()
	c.isReady = true
	callback := c.onReady
	c.readyMu.Unlock()
	
	if callback != nil {
		go callback()
	}
	
	return nil
}

// discoverAPIResources discovers all available API resources and categorizes them
func (c *Controller) discoverAPIResources() error {
	c.logger.Info("controller", "Discovering API resources")

	// Get API groups
	apiGroups, err := c.client.Discovery.ServerGroups()
	if err != nil {
		return fmt.Errorf("failed to discover API groups: %w", err)
	}

	c.logger.Info("controller", fmt.Sprintf("Found %d API groups", len(apiGroups.Groups)))

	// Process core API group (v1)
	if err := c.processAPIGroup("", "v1"); err != nil {
		c.logger.Warning("controller", fmt.Sprintf("Failed to process core API group: %v", err))
	}

	// Process other API groups - discover ALL versions, not just preferred
	for _, group := range apiGroups.Groups {
		c.logger.Debug("controller", fmt.Sprintf("Processing API group %s with %d versions", group.Name, len(group.Versions)))

		// Process ALL versions of this group to catch all CRDs
		for _, version := range group.Versions {
			if err := c.processAPIGroup(group.Name, version.Version); err != nil {
				c.logger.Debug("controller", fmt.Sprintf("Failed to process API group %s/%s: %v", group.Name, version.Version, err))
			}
		}
	}

	c.discoveredResourcesMu.RLock()
	resourceCount := len(c.discoveredResources)
	c.discoveredResourcesMu.RUnlock()
	c.logger.Info("controller", fmt.Sprintf("Discovery completed: %d resources found", resourceCount))
	return nil
}

// processAPIGroup processes a single API group and stores resource information
func (c *Controller) processAPIGroup(group, version string) error {
	var groupVersion string
	if group == "" {
		groupVersion = version // Core API
	} else {
		groupVersion = fmt.Sprintf("%s/%s", group, version)
	}

	resources, err := c.client.Discovery.ServerResourcesForGroupVersion(groupVersion)
	if err != nil {
		return fmt.Errorf("failed to get resources for %s: %w", groupVersion, err)
	}

	c.logger.Debug("controller", fmt.Sprintf("Processing API group %s with %d resources", groupVersion, len(resources.APIResources)))

	for _, resource := range resources.APIResources {


		// Create GVR key
		var gvrKey string
		if group == "" {
			gvrKey = fmt.Sprintf("v1/%s", resource.Name)
		} else {
			gvrKey = fmt.Sprintf("%s/%s/%s", group, version, resource.Name)
		}

		resourceInfo := &ResourceInfo{
			Group:      group,
			Version:    version,
			Resource:   resource.Name,
			Kind:       resource.Kind,
			Namespaced: resource.Namespaced,
		}

		// Avoid overwriting if we already have this exact GVR (from previous version processing)
		c.discoveredResourcesMu.Lock()
		if _, exists := c.discoveredResources[gvrKey]; !exists {
			c.discoveredResources[gvrKey] = resourceInfo
			c.logger.Debug("controller", fmt.Sprintf("Discovered resource: %s (Kind: %s, Namespaced: %t)",
				gvrKey, resource.Kind, resource.Namespaced))
		}
		c.discoveredResourcesMu.Unlock()
	}

	return nil
}

// isResourceWatchable checks if a resource supports watch or list operations
func (c *Controller) isResourceWatchable(resource metav1.APIResource) bool {
	// Loop over resource verbs and return true if "watch" or "list" is found
	for _, verb := range resource.Verbs {
		if verb == "watch" || verb == "list" {
			return true
		}
	}
	
	// If we reach here, neither "watch" nor "list" verb was found
	return false
}

// startCRDWatcher starts a CRD informer to watch for new CustomResourceDefinitions
func (c *Controller) startCRDWatcher() error {
	c.logger.Info("controller", "Starting dynamic CRD watcher for runtime CRD discovery")

	// Create factory for CRD resources (cluster-scoped, no namespace filter) - pure event-driven, no resync needed
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		c.client.Dynamic, 0, "", nil)

	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}

	crdInformer := factory.ForResource(crdGVR).Informer()

	// Add CRD event handlers for dynamic informer management with error handling
	crdInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if unstructuredObj, ok := obj.(*unstructured.Unstructured); ok {
				c.handleCRDAdded(unstructuredObj)
			} else {
				c.logger.Error("controller", "Received unexpected object type in CRD AddFunc")
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if oldUnstructured, ok := oldObj.(*unstructured.Unstructured); ok {
				if newUnstructured, ok := newObj.(*unstructured.Unstructured); ok {
					c.handleCRDUpdated(oldUnstructured, newUnstructured)
				} else {
					c.logger.Error("controller", "Received unexpected new object type in CRD UpdateFunc")
				}
			} else {
				c.logger.Error("controller", "Received unexpected old object type in CRD UpdateFunc")
			}
		},
		DeleteFunc: func(obj interface{}) {
			var unstructuredObj *unstructured.Unstructured
			var ok bool
			
			// Handle Kubernetes cache tombstone objects properly for CRDs
			if tombstone, isTombstone := obj.(cache.DeletedFinalStateUnknown); isTombstone {
				unstructuredObj, ok = tombstone.Obj.(*unstructured.Unstructured)
				if !ok {
					c.logger.Error("controller", "Tombstone contained unexpected object type in CRD DeleteFunc")
					return
				}
			} else {
				unstructuredObj, ok = obj.(*unstructured.Unstructured)
				if !ok {
					c.logger.Error("controller", "Received unexpected object type in CRD DeleteFunc")
					return
				}
			}
			
			c.handleCRDDeleted(unstructuredObj)
		},
	})

	// Start CRD informer in its own goroutine
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.logger.Info("controller", "Running dynamic CRD discovery informer")
		crdInformer.Run(c.ctx.Done())
		c.logger.Info("controller", "Dynamic CRD discovery informer stopped")
	}()

	// Set up callback-driven CRD sync detection using sync.Once
	var crdSyncOnce sync.Once
	crdInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			// Check if this is the first event after sync
			if crdInformer.HasSynced() {
				crdSyncOnce.Do(func() {
					c.logger.Info("controller", "Dynamic CRD watcher sync completed")
				})
			}
		},
	})

	c.logger.Info("controller", "Dynamic CRD watcher started - sync detection active")
	return nil
}

// handleCRDAdded processes newly added CRDs and starts informers if they match configuration
func (c *Controller) handleCRDAdded(crdUnstructured *unstructured.Unstructured) {
	var crd apiextensionsv1.CustomResourceDefinition
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(crdUnstructured.Object, &crd); err != nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to convert CRD: %v", err))
		return
	}

	// Build GVR string for this CRD to check if it was already discovered
	if len(crd.Spec.Versions) == 0 {
		c.logger.Warning("controller", fmt.Sprintf("CRD %s has no versions, cannot process", crd.Name))
		return
	}

	group := crd.Spec.Group
	version := crd.Spec.Versions[0].Name
	resource := crd.Spec.Names.Plural
	gvrString := fmt.Sprintf("%s/%s/%s", group, version, resource)

	// Check if this exact CRD (same group/version/resource) was already discovered during initial API discovery
	c.discoveredResourcesMu.RLock()
	_, alreadyDiscovered := c.discoveredResources[gvrString]
	c.discoveredResourcesMu.RUnlock()
	if alreadyDiscovered {
		c.logger.Debug("controller", fmt.Sprintf("CRD %s (exact GVR: %s) already discovered during startup, skipping", crd.Name, gvrString))
		return
	}

	c.logger.Info("controller", fmt.Sprintf("New CRD detected: %s (GVR: %s)", crd.Name, gvrString))

	// CRD evaluation removed - library users should implement CRD discovery if needed
}

// handleCRDUpdated processes CRD updates
func (c *Controller) handleCRDUpdated(oldCRD, newCRD *unstructured.Unstructured) {
	var oldCRDTyped, newCRDTyped apiextensionsv1.CustomResourceDefinition

	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(oldCRD.Object, &oldCRDTyped); err != nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to convert old CRD: %v", err))
		return
	}

	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(newCRD.Object, &newCRDTyped); err != nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to convert updated CRD: %v", err))
		return
	}

	c.logger.Debug("controller", fmt.Sprintf("CRD updated: %s", newCRDTyped.Name))

	// Check if the update affects our monitoring (GVR or scope changes)
	if c.crdUpdateRequiresRestart(&oldCRDTyped, &newCRDTyped) {
		c.logger.Info("controller", fmt.Sprintf("CRD %s update requires informer restart", newCRDTyped.Name))
		c.handleCRDDeleted(oldCRD)
		c.handleCRDAdded(newCRD)
	} else {
		c.logger.Debug("controller", fmt.Sprintf("CRD %s update doesn't affect monitoring, no restart needed", newCRDTyped.Name))
	}
}

// crdUpdateRequiresRestart determines if a CRD update requires informer restart
func (c *Controller) crdUpdateRequiresRestart(oldCRD, newCRD *apiextensionsv1.CustomResourceDefinition) bool {
	// Check if group changed (highly unlikely but possible)
	if oldCRD.Spec.Group != newCRD.Spec.Group {
		return true
	}

	// Check if scope changed (namespace-scoped to cluster-scoped or vice versa)
	if oldCRD.Spec.Scope != newCRD.Spec.Scope {
		return true
	}

	// Check if resource names changed
	if oldCRD.Spec.Names.Plural != newCRD.Spec.Names.Plural {
		return true
	}

	// Check if any monitored versions changed
	oldVersions := make(map[string]bool)
	for _, version := range oldCRD.Spec.Versions {
		if version.Served && version.Storage {
			oldVersions[version.Name] = true
		}
	}

	newVersions := make(map[string]bool)
	for _, version := range newCRD.Spec.Versions {
		if version.Served && version.Storage {
			newVersions[version.Name] = true
		}
	}

	// Check if the set of served+storage versions changed
	if len(oldVersions) != len(newVersions) {
		return true
	}

	for version := range oldVersions {
		if !newVersions[version] {
			return true
		}
	}

	// No significant changes detected
	return false
}

// handleCRDDeleted processes CRD deletions and stops corresponding informers
func (c *Controller) handleCRDDeleted(crdUnstructured *unstructured.Unstructured) {
	var crd apiextensionsv1.CustomResourceDefinition
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(crdUnstructured.Object, &crd); err != nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to convert deleted CRD: %v", err))
		return
	}

	c.logger.Info("controller", fmt.Sprintf("CRD deleted: %s", crd.Name))

	// Stop any running informers for this CRD
	c.stopCRDInformer(&crd)
}


// selectCRDVersion selects the most appropriate version for monitoring a CRD
// Priority: 1) Storage version 2) Served version 3) First version
func (c *Controller) selectCRDVersion(crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinitionVersion, error) {
	if len(crd.Spec.Versions) == 0 {
		return nil, fmt.Errorf("CRD %s has no versions", crd.Name)
	}

	// Strategy 1: Look for stored versions in status (most reliable)
	if len(crd.Status.StoredVersions) > 0 {
		// Find the first stored version that is also served
		for _, storedVersion := range crd.Status.StoredVersions {
			for _, specVersion := range crd.Spec.Versions {
				if specVersion.Name == storedVersion && specVersion.Served {
					c.logger.Debug("controller", fmt.Sprintf("Selected stored+served version %s for CRD %s", specVersion.Name, crd.Name))
					return &specVersion, nil
				}
			}
		}
		c.logger.Debug("controller", fmt.Sprintf("No served stored versions found for CRD %s, falling back to storage flag", crd.Name))
	}

	// Strategy 2: Look for version marked as storage=true and served=true in spec
	for _, version := range crd.Spec.Versions {
		if version.Storage && version.Served {
			c.logger.Debug("controller", fmt.Sprintf("Selected storage+served version %s for CRD %s", version.Name, crd.Name))
			return &version, nil
		}
	}

	// Strategy 3: Look for any served version
	for _, version := range crd.Spec.Versions {
		if version.Served {
			c.logger.Debug("controller", fmt.Sprintf("Selected served version %s for CRD %s", version.Name, crd.Name))
			return &version, nil
		}
	}

	// Strategy 4: Fall back to first version (should rarely happen)
	c.logger.Warning("controller", fmt.Sprintf("No served versions found for CRD %s, using first version %s", crd.Name, crd.Spec.Versions[0].Name))
	return &crd.Spec.Versions[0], nil
}


// createNamespaceSpecificInformer creates an informer for a specific namespace
func (c *Controller) createNamespaceSpecificInformer(config InformerConfig, namespace string, normalizedConfigs []NormalizedConfig) (cache.SharedIndexInformer, error) {
	c.logger.Info("controller", fmt.Sprintf("Starting namespace-specific informer for %s (namespace: %s)", config.GVRString, namespace))
	
	// Simple selector application - complex interpretation removed
	// Library users should implement their own selector logic via middleware
	var tweakListOptions func(*metav1.ListOptions)
	if len(normalizedConfigs) > 0 && normalizedConfigs[0].LabelSelector != "" {
		labelSelector := normalizedConfigs[0].LabelSelector
		tweakListOptions = func(options *metav1.ListOptions) {
			options.LabelSelector = labelSelector
		}
	}

	// Create dynamic informer factory with namespace-specific filtering - pure event-driven, no resync needed
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		c.client.Dynamic, 0, namespace, tweakListOptions)
	
	// Get informer
	informer := factory.ForResource(config.GVR).Informer()
	if informer == nil {
		return nil, fmt.Errorf("failed to create namespace-specific informer for %s", config.GVRString)
	}

	// Store the lister for later retrieval by workers
	lister := factory.ForResource(config.GVR).Lister()
	// CRITICAL FIX: Use namespace-specific key to avoid overwriting listers from other namespaces
	listerKey := config.GVRString + "@" + namespace
	c.listers.Store(listerKey, lister)

	// Create state tracker
	tracker := &InformerStateTracker{
		GVR:    listerKey, // Use the same namespace-specific key
		Lister: lister,
	}
	c.informerTrackers.Store(listerKey, tracker)
	
	// Notify metrics of informer creation
	c.metrics.OnInformerCreated(config.GVRString, config.Scope)
	
	// Hook into informer sync completion via callback
	c.setupSyncCallback(informer, tracker, config)
	
	// Add state-tracking event handlers
	informer.AddEventHandler(c.createStateTrackingEventHandlers(tracker, config))
	
	c.logger.Info("controller", fmt.Sprintf("Running namespace-specific informer for %s (namespace: %s)", config.GVRString, namespace))
	return informer, nil
}

// InformerStartParams contains parameters for starting different types of informers
type InformerStartParams struct {
	GVR               schema.GroupVersionResource
	Scope             apiextensionsv1.ResourceScope
	GVRString         string
	Name              string
	InformerKey       string // For namespace-specific informers (optional)
	Namespace         string // For namespace-specific informers (optional)
	NormalizedConfigs []NormalizedConfig // For CRD and namespace-specific informers (optional)
	HandlerFunc       func(string, *unstructured.Unstructured) // Event handler function
	Description       string // For logging
}

// startUnifiedInformer is a unified function that replaces startDynamicCRDInformer, startBuiltinInformer, and startNamespaceSpecificInformer
func (c *Controller) startUnifiedInformer(params InformerStartParams) {
	defer c.wg.Done()
	
	// Determine which key to use for active informer tracking
	trackingKey := params.InformerKey
	if trackingKey == "" {
		trackingKey = params.GVRString
	}
	defer c.activeInformers.Delete(trackingKey)

	// Create informer config
	config := InformerConfig{
		GVR:         params.GVR,
		Scope:       params.Scope,
		GVRString:   params.GVRString,
		Context:     c.ctx,
		Name:        params.Name,
		HandlerFunc: params.HandlerFunc,
	}
	
	// Create informer using appropriate factory
	// UNIFIED PATH: Always use createNamespaceSpecificInformer for consistent lister key strategy
	// For cluster-scoped resources, params.Namespace will be "" which is handled correctly
	informer, err := c.createNamespaceSpecificInformer(config, params.Namespace, params.NormalizedConfigs)
	
	if err != nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to create %s: %v", params.Description, err))
		return
	}
	
	// Run with consistent logging
	c.runInformerWithLogging(informer, c.ctx, params.Description)
}

// stopCRDInformer stops the informer for a specific CRD
func (c *Controller) stopCRDInformer(crd *apiextensionsv1.CustomResourceDefinition) {
	c.logger.Info("controller", fmt.Sprintf("Stopping informer for deleted CRD: %s", crd.Name))

	// Declare variables for later use
	var group, version, resource, gvrString string

	// Select version to build consistent GVR string
	selectedVersion, err := c.selectCRDVersion(crd)
	if err != nil {
		c.logger.Warning("controller", fmt.Sprintf("Failed to select version for CRD %s during stop: %v", crd.Name, err))
		return
	}

	// Convert CRD to GVR string for consistent key lookup
	gvrString = fmt.Sprintf("%s/%s/%s", crd.Spec.Group, selectedVersion.Name, crd.Spec.Names.Plural)

	// Get cancel function and stop the informer gracefully using GVR string
	if cancelFunc, exists := c.cancellers.LoadAndDelete(gvrString); exists {
		if cancel, ok := cancelFunc.(context.CancelFunc); ok {
			c.logger.Debug("controller", fmt.Sprintf("Cancelling context for CRD %s (GVR: %s)", crd.Name, gvrString))
			cancel()
			c.logger.Info("controller", fmt.Sprintf("Gracefully stopped dynamic informer for CRD %s", crd.Name))
		} else {
			c.logger.Warning("controller", fmt.Sprintf("Invalid cancel function type for CRD %s", crd.Name))
		}
	} else {
		c.logger.Debug("controller", fmt.Sprintf("No active informer found for CRD %s (may not have matched configuration)", crd.Name))
	}

	// Remove from discovered resources - handle potential edge cases
	if len(crd.Spec.Versions) == 0 {
		c.logger.Warning("controller", fmt.Sprintf("CRD %s has no versions, cannot clean up discovered resources", crd.Name))
		return
	}

	group = crd.Spec.Group
	version = crd.Spec.Versions[0].Name
	resource = crd.Spec.Names.Plural
	gvrString = fmt.Sprintf("%s/%s/%s", group, version, resource)

	c.discoveredResourcesMu.Lock()
	if _, exists := c.discoveredResources[gvrString]; exists {
		delete(c.discoveredResources, gvrString)
		c.logger.Debug("controller", fmt.Sprintf("Removed %s from discovered resources", gvrString))
	} else {
		c.logger.Debug("controller", fmt.Sprintf("Resource %s not found in discovered resources (already cleaned up)", gvrString))
	}
	c.discoveredResourcesMu.Unlock()
}

// startConfigDrivenInformers starts informers based on config and discovery results
func (c *Controller) startConfigDrivenInformers() error {
	c.logger.Info("controller", "Starting config-driven informers for resources")

	// Normalize configuration to unified internal structure
	normalizedGVRs, err := c.config.Normalize()
	if err != nil {
		return fmt.Errorf("failed to normalize configuration: %w", err)
	}

	c.logger.Info("controller", fmt.Sprintf("Normalized configuration: monitoring %d unique GVRs", len(normalizedGVRs)))

	informerCount := 0

	// Start separate informers per namespace+GVR combination
	for gvrString, normalizedConfigs := range normalizedGVRs {
		c.logger.Info("controller", fmt.Sprintf("Processing %s (matches %d configuration entries)", gvrString, len(normalizedConfigs)))

		// Look up resource info from discovery
		c.discoveredResourcesMu.RLock()
		resourceInfo, found := c.discoveredResources[gvrString]
		c.discoveredResourcesMu.RUnlock()
		if !found {
			c.logger.Warning("controller", fmt.Sprintf("Resource %s not found in discovery results, skipping", gvrString))
			continue
		}

		// Create GVR and scope from discovered information
		gvr := schema.GroupVersionResource{
			Group:    resourceInfo.Group,
			Version:  resourceInfo.Version,
			Resource: resourceInfo.Resource,
		}

		var scope apiextensionsv1.ResourceScope
		if resourceInfo.Namespaced {
			scope = apiextensionsv1.NamespaceScoped
		} else {
			scope = apiextensionsv1.ClusterScoped
		}

		// Group configs by namespace to create separate informers
		namespaceGroups := make(map[string][]NormalizedConfig)
		for _, config := range normalizedConfigs {
			if scope == apiextensionsv1.ClusterScoped {
				// For cluster-scoped resources, ignore NamespaceNames and use cluster-scoped grouping
				namespaceGroups["cluster-scoped"] = append(namespaceGroups["cluster-scoped"], config)
			} else {
				// For namespace-scoped resources, group by specified namespaces
				for _, ns := range config.NamespaceNames {
					if ns == "" {
						ns = "cluster-scoped" // Fallback for empty namespace
					}
					namespaceGroups[ns] = append(namespaceGroups[ns], config)
				}
			}
		}

		// Create separate informer for each namespace
		for namespace, configs := range namespaceGroups {
			informerKey := gvrString + "@" + namespace
			
			// Mark this GVR+namespace as having an active informer
			c.activeInformers.Store(informerKey, true)
			
			actualNamespace := namespace
			if namespace == "cluster-scoped" {
				actualNamespace = ""
			}
			
			c.logger.Info("controller", fmt.Sprintf("Setting up informer for %s (namespace: %s)", gvrString, actualNamespace))
			
			// Start separate informer for this namespace+GVR combination
		c.wg.Add(1)
			go c.startUnifiedInformer(InformerStartParams{
				GVR:               gvr,
				Scope:             scope,
				GVRString:         gvrString,
				Name:              informerKey,
				InformerKey:       informerKey,
				Namespace:         actualNamespace,
				NormalizedConfigs: configs,
		HandlerFunc: func(eventType string, obj *unstructured.Unstructured) {
					c.handleNamespaceSpecificEvent(eventType, obj, gvrString, configs)
				},
				Description:       fmt.Sprintf("namespace-specific informer for %s (namespace: %s)", gvrString, actualNamespace),
			})
		informerCount++
		}
	}

	c.logger.Info("controller", fmt.Sprintf("Started %d config-driven informers", informerCount))
	return nil
}




// handleCustomResourceEvent processes events from dynamic CRD informers
func (c *Controller) handleCustomResourceEvent(eventType string, obj *unstructured.Unstructured, crdName string) {
	name := obj.GetName()
	namespace := obj.GetNamespace()
	uid := obj.GetUID()
	kind := obj.GetKind()

	if namespace != "" {
		c.logger.Info("controller", fmt.Sprintf("CUSTOM [%s] %s/%s %s/%s (UID: %s)",
			eventType, crdName, kind, namespace, name, uid))
	} else {
		c.logger.Info("controller", fmt.Sprintf("CUSTOM [%s] %s/%s %s (UID: %s)",
			eventType, crdName, kind, name, uid))
	}

	// Future: Add sophisticated event processing, correlation, file output, etc.
}

// GetActiveInformers returns the count of active informers
func (c *Controller) GetActiveInformers() (config int, dynamic int) {
	// Count config-driven informers
	config = 0
	c.activeInformers.Range(func(key, value interface{}) bool {
		config++
		return true
	})

	// Count dynamic informers
	dynamic = 0
	c.cancellers.Range(func(key, value interface{}) bool {
		dynamic++
		return true
	})

	c.logger.Debug("controller", fmt.Sprintf("Active informers: %d config-driven, %d dynamic", config, dynamic))
	return config, dynamic
}

// Stop gracefully shuts down all informers with timeout
func (c *Controller) Stop() {
	c.logger.Info("controller", "Stopping multi-layered informer controller")

	// Cancel main context - this stops all informers
	c.cancel()

	// Shutdown the work queue to stop workers
	c.workQueue.ShutDown()

	// Stop all dynamic informers explicitly
	dynamicCount := 0
	c.cancellers.Range(func(key, value interface{}) bool {
		if cancel, ok := value.(context.CancelFunc); ok {
			c.logger.Debug("controller", fmt.Sprintf("Stopping dynamic informer: %v", key))
			cancel()
			dynamicCount++
		}
		return true
	})

	if dynamicCount > 0 {
		c.logger.Info("controller", fmt.Sprintf("Cancelled %d dynamic informers", dynamicCount))
	}

	// Wait for all goroutines to finish gracefully - no arbitrary timeout
	c.logger.Info("controller", "Waiting for all informers and workers to stop gracefully...")
		c.wg.Wait()
		c.logger.Info("controller", "All informers and workers stopped gracefully")
	
	// Shutdown metrics server gracefully without timeout
	if c.metrics != nil {
		if err := c.metrics.Shutdown(context.Background()); err != nil {
			c.logger.Error("controller", fmt.Sprintf("Error shutting down metrics server: %v", err))
		} else {
			c.logger.Info("controller", "Metrics server stopped gracefully")
		}
	}
}

// runWorker is a long-running function that will continually call processNextWorkItem
func (c *Controller) runWorker() {
	defer c.wg.Done()
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem will read a single work item off the workqueue and process it
func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workQueue.Get()
	if shutdown {
		return false
	}

	// Always call Done to mark this item as processed
	defer c.workQueue.Done(obj)

	var workItem *WorkItem
	var ok bool
	if workItem, ok = obj.(*WorkItem); !ok {
		// Invalid item, forget it
		c.workQueue.Forget(obj)
		c.logger.Warning("controller", fmt.Sprintf("Expected WorkItem but got %T", obj))
		return true
	}

	// Process the work item
	if err := c.reconcile(workItem); err != nil {
		// Re-queue the item on failure with exponential backoff
		c.workQueue.AddRateLimited(workItem)
		c.logger.Error("controller", fmt.Sprintf("Error processing %s: %v", workItem.Key, err))
		return true
	}

	// Successfully processed, forget the item
	c.workQueue.Forget(workItem)
	return true
}

// reconcile contains the core business logic for processing a work item
func (c *Controller) reconcile(workItem *WorkItem) error {
	// NO CLIENT-SIDE FILTERING - rely entirely on server-side filtering and application logic
	// All events that reach here have already passed server-side filtering

	// At this point, we know the object is one we are configured to watch.

	// Get lister for this GVR - namespace-specific key only, no fallbacks
	namespace, _, keyErr := cache.SplitMetaNamespaceKey(workItem.Key)
	if keyErr != nil {
		c.logger.Error("controller", "Failed to parse workItem key: "+workItem.Key)
		return errors.New("failed to parse workItem key: " + workItem.Key)
	}
	
	namespaceListerKey := workItem.GVRString + "@" + namespace
	listerInterface, exists := c.listers.Load(namespaceListerKey)
	if !exists {
		c.logger.Error("controller", "No lister found for key: "+namespaceListerKey)
		return errors.New("no lister found for key: " + namespaceListerKey)
	}

	lister, ok := listerInterface.(cache.GenericLister)
	if !ok {
		c.logger.Error("controller", "Invalid lister type for GVR "+workItem.GVRString)
		return errors.New("invalid lister type for GVR " + workItem.GVRString)
	}

	obj, err := lister.Get(workItem.Key)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			// Only process as DELETED if the workItem.EventType is actually DELETED
			if workItem.EventType != "DELETED" {
				// Object was deleted after ADDED/UPDATED event was queued - skip processing
				c.logger.Debug("controller", fmt.Sprintf("Skipping %s event for %s %s - object no longer exists", workItem.EventType, workItem.GVRString, workItem.Key))
				return nil
			}
			
			// The object was deleted. Log CONFIG message and call OnMatched handlers.
			c.logger.Info("controller", fmt.Sprintf("CONFIG [DELETED] %s %s", workItem.GVRString, workItem.Key))
			
			// Parse the key to get namespace and name for JSON event
			namespace, name, keyErr := cache.SplitMetaNamespaceKey(workItem.Key)
			if keyErr != nil {
				// For cluster-scoped resources, key is just the name
				name = workItem.Key
				namespace = ""
			}
			
			// Use captured UID and annotations from WorkItem for DELETED events - no fallbacks
			if workItem.DeletedUID == "" {
				c.logger.Error("controller", "No captured UID for DELETED event: "+workItem.Key)
				return errors.New("no captured UID for DELETED event: " + workItem.Key)
			}
			
			uid := workItem.DeletedUID
			annotations := workItem.DeletedAnnotations
			c.logger.Debug("controller", fmt.Sprintf("Using captured DELETED metadata: UID=%s, annotations=%d", uid, len(annotations)))
			
			// Create a minimal object with captured annotations for DELETED events
			var deletedObjForLogging *unstructured.Unstructured
			if annotations != nil {
				deletedObjForLogging = &unstructured.Unstructured{}
				deletedObjForLogging.SetName(name)
				if namespace != "" {
					deletedObjForLogging.SetNamespace(namespace)
				}
				if uid != "" && uid != "unknown" {
					deletedObjForLogging.SetUID(types.UID(uid))
				}
				deletedObjForLogging.SetAnnotations(annotations)
			}
			
			// Log JSON event for DELETE with captured metadata
			c.logJSONEvent("DELETED", workItem.GVRString, namespace, name, uid, nil, deletedObjForLogging)
			
			// Clean up UID from cache after processing
			c.cleanupUIDFromInformerState(workItem.GVRString, namespace, name)
			
			// Create a minimal unstructured object for DELETE events
			// We can't get the full object since it's deleted, but we can extract key info
			deletedObj := &unstructured.Unstructured{}
			deletedObj.SetName(name)
			if namespace != "" {
				deletedObj.SetNamespace(namespace)
			}
			
			// Call OnMatched handlers for DELETE events
			for _, config := range workItem.Configs {
				// RACE CONDITION FIX: Create a deep copy for event handlers to avoid concurrent access
				matchedEvent := MatchedEvent{
					EventType: "DELETED",
					Object:    deletedObj.DeepCopy(), // Deep copy to prevent concurrent access by event handlers
					GVR:       workItem.GVRString,
					Key:       workItem.Key,
					Config:    config,
					Timestamp: time.Now(), // DELETE events don't have the full object, so use current time
				}
				
				// Call event handlers (non-blocking)
				c.handlersMu.RLock()
				handlers := c.eventHandlers
				c.handlersMu.RUnlock()
				
				for _, handler := range handlers {
					// Call handler in goroutine to avoid blocking Faro
					go func(h EventHandler, event MatchedEvent) {
						if err := h.OnMatched(event); err != nil {
							c.logger.Warning("controller", fmt.Sprintf("Event handler failed for DELETE: %v", err))
						}
					}(handler, matchedEvent)
				}
				break // Only process once per object
			}
			
			return nil
		}
		return fmt.Errorf("failed to get object %s: %w", workItem.Key, err)
	}

	unstructuredObj, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("failed to convert object %s to unstructured", workItem.Key)
	}

	// Apply detailed logging logic for ADD/UPDATE
	return c.processObject(workItem.EventType, unstructuredObj, workItem.GVRString, workItem.Configs)
}

// processObject contains the core filtering and logging logic
func (c *Controller) processObject(eventType string, obj *unstructured.Unstructured, gvrString string, configs []NormalizedConfig) error {
	resourceName := obj.GetName()
	resourceNamespace := obj.GetNamespace()
	resourceUID := obj.GetUID()

	// Apply namespace filtering when watching all namespaces
	for _, config := range configs {
		// Check if this config matches the resource's namespace
		namespaceMatches := false
		if len(config.NamespaceNames) == 0 {
			// No namespace names means match all namespaces
			namespaceMatches = true
		} else {
			// Check if resource namespace matches any of the configured names
			for _, namespaceName := range config.NamespaceNames {
				if namespaceName == "" {
					// Empty name means all namespaces
					namespaceMatches = true
					break
				} else if namespaceName == resourceNamespace {
					// Exact namespace match
					namespaceMatches = true
					break
				}
			}
		}
		
		// Skip this config if namespace doesn't match
		if !namespaceMatches {
			continue
		}
		
		// Create matched event for handlers
		// RACE CONDITION FIX: Create a deep copy for event handlers to avoid concurrent access
		matchedEvent := MatchedEvent{
			EventType: eventType,
			Object:    obj.DeepCopy(), // Deep copy to prevent concurrent access by event handlers
			GVR:       gvrString,
			Key:       obj.GetNamespace() + "/" + obj.GetName(),
			Config:    config,
			Timestamp: obj.GetCreationTimestamp().Time,
		}
		
		// For cluster-scoped resources, key is just the name
		if resourceNamespace == "" {
			matchedEvent.Key = resourceName
		}
		
		// Call event handlers (non-blocking)
		c.handlersMu.RLock()
		handlers := c.eventHandlers
		c.handlersMu.RUnlock()
		
		for _, handler := range handlers {
			// Call handler in goroutine to avoid blocking Faro
			go func(h EventHandler, event MatchedEvent) {
				if err := h.OnMatched(event); err != nil {
					c.logger.Warning("controller", fmt.Sprintf("Event handler failed: %v", err))
				}
			}(handler, matchedEvent)
		}
		
		// Log the matched event (preserve existing behavior)
		if resourceNamespace != "" {
			c.logger.Info("controller", fmt.Sprintf("CONFIG [%s] %s %s/%s (UID: %s, namespace: %s)",
				eventType, gvrString, resourceNamespace, resourceName, resourceUID, config.GVR))
		} else {
			c.logger.Info("controller", fmt.Sprintf("CONFIG [%s] %s %s (UID: %s, namespace: %s)",
				eventType, gvrString, resourceName, resourceUID, config.GVR))
		}
		
		// Log JSON event for export
		c.logJSONEvent(eventType, gvrString, resourceNamespace, resourceName, string(resourceUID), obj.GetLabels(), obj)
		
		break // Only process once per object
	}

	return nil
}

// REMOVED: All client-side filtering functions have been eliminated from Faro core
// Old event handler functions removed - replaced by handleUnifiedNormalizedEvent()


// handleNamespaceSpecificEvent processes events from namespace-specific informers
func (c *Controller) handleNamespaceSpecificEvent(eventType string, obj *unstructured.Unstructured, gvrString string, configs []NormalizedConfig) {
	// Use the same event handling as the unified informer
	c.handleUnifiedNormalizedEvent(eventType, obj, gvrString, configs)
}

// handleUnifiedNormalizedEvent processes events with multiple normalized config-based filtering
// handleUnifiedNormalizedEvent is a lightweight event handler that only enqueues work items
func (c *Controller) handleUnifiedNormalizedEvent(eventType string, obj *unstructured.Unstructured, gvrString string, normalizedConfigs []NormalizedConfig) {
	// Extract the object key - this is the only work done in the event handler
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to get key for object: %v", err))
		return
	}

	// Create work item and add to queue
	workItem := &WorkItem{
		Key:       key,
		GVRString: gvrString,
		Configs:   normalizedConfigs,
		EventType: eventType,
	}

	// For DELETED events, capture UID and annotations before they're lost
	if eventType == "DELETED" && obj != nil {
		workItem.DeletedUID = string(obj.GetUID())
		workItem.DeletedAnnotations = obj.GetAnnotations()
		c.logger.Debug("controller", fmt.Sprintf("Captured DELETED metadata: UID=%s, annotations=%d", workItem.DeletedUID, len(workItem.DeletedAnnotations)))
	}

	c.logger.Debug("controller", fmt.Sprintf("Queueing %s event for %s %s", eventType, gvrString, key))
	c.workQueue.Add(workItem)
}

