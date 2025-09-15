package faro

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	Key       string             // Object key (namespace/name or name)
	GVRString string             // Group/Version/Resource identifier
	Configs   []NormalizedConfig // Configuration rules that apply to this GVR
	EventType string             // ADDED, UPDATED, DELETED
}

// MatchedEvent represents a filtered event that matched configuration patterns
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
	
	// Additional fields for v1/events - dynamic like labels
	InvolvedObject map[string]interface{} `json:"involvedObject,omitempty"`
	Reason         string                 `json:"reason,omitempty"`
	Message        string                 `json:"message,omitempty"`
	Type           string                 `json:"type,omitempty"`
}

// EventHandler interface for handling matched events via callbacks
type EventHandler interface {
	OnMatched(event MatchedEvent) error
}



// logJSONEvent creates and logs a structured JSON event
func (c *Controller) logJSONEvent(eventType, gvr, namespace, name, uid string, labels map[string]string, obj *unstructured.Unstructured) {
	var annotations map[string]string
	var timestamp string
	var objCopy *unstructured.Unstructured
	
	if obj != nil {
		// RACE CONDITION FIX: Create a deep copy to avoid concurrent map access
		// The original object might be modified by other goroutines (informers, controllers, etc.)
		objCopy = obj.DeepCopy()
		annotations = objCopy.GetAnnotations()
		timestamp = objCopy.GetCreationTimestamp().UTC().Format(time.RFC3339Nano)
	} else {
		timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	
	jsonEvent := JSONEvent{
		Timestamp:   timestamp,
		EventType:   eventType,
		GVR:         gvr,
		Namespace:   namespace,
		Name:        name,
		UID:         uid,
		Labels:      labels,
		Annotations: annotations,
	}

	// Special handling for v1/events to extract involvedObject information
	if gvr == "v1/events" && objCopy != nil {
		if involvedObj, found, _ := unstructured.NestedMap(objCopy.Object, "involvedObject"); found {
			jsonEvent.InvolvedObject = involvedObj
		}
		jsonEvent.Reason, _, _ = unstructured.NestedString(objCopy.Object, "reason")
		jsonEvent.Message, _, _ = unstructured.NestedString(objCopy.Object, "message")
		jsonEvent.Type, _, _ = unstructured.NestedString(objCopy.Object, "type")
	}

	jsonData, err := json.Marshal(jsonEvent)
	if err != nil {
		c.logger.Warning("controller", fmt.Sprintf("Failed to marshal JSON event: %v", err))
		return
	}

	// Log as JSON for the JSONFileHandler to pick up
	c.logger.Debug("controller", string(jsonData))
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

	// Track builtin informer count
	builtinCount int

	// Event handlers for library usage
	eventHandlers []EventHandler
	handlersMu    sync.RWMutex

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

// StartNewInformers starts informers only for newly added GVRs that don't have active informers
func (c *Controller) StartNewInformers() error {
	c.logger.Info("controller", "Starting informers for newly added GVRs")
	return c.startConfigDrivenInformers()
}

// createGenericInformer creates a generic informer with consistent setup
func (c *Controller) createGenericInformer(config InformerConfig) (cache.SharedIndexInformer, error) {
	// Handle namespace scope logic
	var namespace string
	if config.Scope == apiextensionsv1.NamespaceScoped {
		namespace = "" // Watch all namespaces, filter in event handler (generic informer doesn't have config patterns)
	}

	// Create factory
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		c.client.Dynamic, 10*time.Minute, namespace, nil)
	
	// Get informer
	informer := factory.ForResource(config.GVR).Informer()
	if informer == nil {
		return nil, fmt.Errorf("failed to create informer for %s", config.GVRString)
	}

	// Add generic event handlers
	informer.AddEventHandler(c.createEventHandlers(config.HandlerFunc, config.GVRString))
	
	return informer, nil
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

// createLabelSelectorInformer creates an informer with label selector support for the normalized config path
func (c *Controller) createLabelSelectorInformer(config InformerConfig, normalizedConfigs []NormalizedConfig) (cache.SharedIndexInformer, error) {
	// Handle namespace scope logic with server-side filtering
	var namespace string
	if config.Scope == apiextensionsv1.NamespaceScoped {
		// Collect all unique namespaces from all configs
		allNamespaces := make(map[string]bool)
		for _, nConfig := range normalizedConfigs {
			for _, ns := range nConfig.NamespacePatterns {
				if ns != "" { // Skip empty namespace patterns
					allNamespaces[ns] = true
				}
			}
		}
		
		// If we have exactly one namespace, use server-side filtering for that namespace
		if len(allNamespaces) == 1 {
			for ns := range allNamespaces {
				namespace = ns
				c.logger.Info("controller", fmt.Sprintf("Single namespace config for %s: using server-side namespace filtering: %s", config.GVRString, namespace))
				break
			}
		} else if len(allNamespaces) > 1 {
			// Multiple namespaces - watch all namespaces, server will handle other filtering
			namespace = ""
			namespaceList := make([]string, 0, len(allNamespaces))
			for ns := range allNamespaces {
				namespaceList = append(namespaceList, ns)
			}
			c.logger.Info("controller", fmt.Sprintf("Multi-namespace config for %s: watching all namespaces, server-side filtering for %v", config.GVRString, namespaceList))
		} else {
			// No specific namespaces - watch all namespaces
			namespace = ""
			c.logger.Info("controller", fmt.Sprintf("No namespace patterns for %s: watching all namespaces", config.GVRString))
		}
	}

	// Determine the label selector to use for this GVR (for server-side filtering)
	var labelSelector string
	for _, nConfig := range normalizedConfigs {
		if nConfig.LabelSelector != "" {
			labelSelector = nConfig.LabelSelector
			break // Use first label selector found
		}
	}

	// Determine the field selector for name pattern (for server-side filtering)
	var fieldSelector string
	for _, nConfig := range normalizedConfigs {
		c.logger.Debug("controller", fmt.Sprintf("Checking normalized config - NamePattern: '%s'", nConfig.NamePattern))
		if nConfig.NamePattern != "" {
			// For exact name matches, use field selector
			if !strings.ContainsAny(nConfig.NamePattern, ".*+?^${}[]|()\\") {
				fieldSelector = fmt.Sprintf("metadata.name=%s", nConfig.NamePattern)
				c.logger.Info("controller", fmt.Sprintf("Using server-side name filtering for %s: %s", config.GVRString, fieldSelector))
				break
			} else {
				c.logger.Warning("controller", fmt.Sprintf("Regex name patterns not supported for server-side filtering: %s", nConfig.NamePattern))
			}
		}
	}

	// Create a tweakListOptions function to apply selectors
	tweakListOptions := func(options *metav1.ListOptions) {
		if labelSelector != "" {
			options.LabelSelector = labelSelector
			c.logger.Debug("controller", fmt.Sprintf("Applying label selector '%s' to informer for %s", labelSelector, config.GVRString))
		}
		if fieldSelector != "" {
			options.FieldSelector = fieldSelector
			c.logger.Debug("controller", fmt.Sprintf("Applying field selector '%s' to informer for %s", fieldSelector, config.GVRString))
		}
	}

	// Create dynamic informer factory with label selector filtering
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		c.client.Dynamic, 10*time.Minute, namespace, tweakListOptions)
	
	// Get informer
	informer := factory.ForResource(config.GVR).Informer()
	if informer == nil {
		return nil, fmt.Errorf("failed to create informer for %s", config.GVRString)
	}

	// Store the lister for later retrieval by workers
	lister := factory.ForResource(config.GVR).Lister()
	c.listers.Store(config.GVRString, lister)

	// Add generic event handlers
	informer.AddEventHandler(c.createEventHandlers(config.HandlerFunc, config.GVRString))
	
	return informer, nil
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
	if err := c.startConfigDrivenInformers(); err != nil {
		return fmt.Errorf("failed to start config-driven informers: %w", err)
	}

	// 3. Start dynamic CRD watcher for runtime CRD discovery
	if err := c.startCRDWatcher(); err != nil {
		return fmt.Errorf("failed to start CRD watcher: %w", err)
	}

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
		if strings.Contains(resource.Name, "/") {
			continue // Skip subresources
		}

		// Skip resources that don't support watch operations
		if !c.isResourceWatchable(resource) {
			c.logger.Debug("controller", fmt.Sprintf("Skipping non-watchable resource: %s/%s/%s (verbs: %v)", 
				group, version, resource.Name, resource.Verbs))
			continue
		}

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

	// Create factory for CRD resources (cluster-scoped, no namespace filter)
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		c.client.Dynamic, 10*time.Minute, "", nil)

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

	// Wait for CRD informer cache to sync
	if !cache.WaitForCacheSync(c.ctx.Done(), crdInformer.HasSynced) {
		return fmt.Errorf("failed to sync CRD informer cache")
	}

	// After sync, perform a reconciliation to handle any race conditions
	c.logger.Info("controller", "Dynamic CRD watcher synced, performing startup reconciliation")
	if err := c.reconcileStartupCRDs(); err != nil {
		c.logger.Warning("controller", fmt.Sprintf("Startup CRD reconciliation completed with warnings: %v", err))
	}

	c.logger.Info("controller", "Dynamic CRD watcher started and synced")
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

	// Check if this CRD matches any of our configured patterns
	c.evaluateAndStartCRDInformer(&crd)
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

// evaluateAndStartCRDInformer checks if a CRD matches configuration and starts informers using multi-namespace approach
func (c *Controller) evaluateAndStartCRDInformer(crd *apiextensionsv1.CustomResourceDefinition) {
	if len(crd.Spec.Versions) == 0 {
		c.logger.Warning("controller", fmt.Sprintf("CRD %s has no versions, skipping", crd.Name))
		return
	}

	// Select the appropriate version using Kubernetes best practices
	selectedVersion, err := c.selectCRDVersion(crd)
	if err != nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to select version for CRD %s: %v", crd.Name, err))
		return
	}

	// Build GVR for this CRD using the selected version
	group := crd.Spec.Group
	version := selectedVersion.Name
	resource := crd.Spec.Names.Plural

	gvrString := fmt.Sprintf("%s/%s/%s", group, version, resource)

	c.logger.Debug("controller", fmt.Sprintf("Evaluating CRD %s (GVR: %s)", crd.Name, gvrString))

	// Add to discovered resources first
	resourceInfo := &ResourceInfo{
		Group:      group,
		Version:    version,
		Resource:   resource,
		Kind:       crd.Spec.Names.Kind,
		Namespaced: crd.Spec.Scope == apiextensionsv1.NamespaceScoped,
	}
	c.discoveredResourcesMu.Lock()
	c.discoveredResources[gvrString] = resourceInfo
	c.discoveredResourcesMu.Unlock()

	// Build GVR and scope
	gvr := schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	}

	var scope apiextensionsv1.ResourceScope
	if crd.Spec.Scope == apiextensionsv1.NamespaceScoped {
		scope = apiextensionsv1.NamespaceScoped
	} else {
		scope = apiextensionsv1.ClusterScoped
	}

	// Convert NamespaceConfig to NormalizedConfig for consistency with regular informers
	var normalizedConfigs []NormalizedConfig
	
	// Check if this GVR matches any configured patterns and collect matching configs
	for _, nsConfig := range c.config.Namespaces {
		if _, exists := nsConfig.Resources[gvrString]; exists {
			c.logger.Info("controller", fmt.Sprintf("CRD %s matches configuration in namespace pattern %s", crd.Name, nsConfig.NamePattern))
			
			// Convert to NormalizedConfig
			normalizedConfig := NormalizedConfig{
				GVR:               gvrString,
				NamespacePatterns: []string{nsConfig.NamePattern},
				NamePattern:       "", // CRDs don't have name patterns in this context
				LabelSelector:     "", // CRDs don't have label selectors in this context
			}
			normalizedConfigs = append(normalizedConfigs, normalizedConfig)
		}
	}

	// Also check ResourceConfig for direct GVR matches
	for _, resConfig := range c.config.Resources {
		if resConfig.GVR == gvrString {
			c.logger.Info("controller", fmt.Sprintf("CRD %s matches direct resource configuration", crd.Name))
			
			// Convert to NormalizedConfig
			normalizedConfig := NormalizedConfig{
				GVR:               gvrString,
				NamespacePatterns: resConfig.NamespacePatterns,
				NamePattern:       resConfig.NamePattern,
				LabelSelector:     resConfig.LabelSelector,
			}
			normalizedConfigs = append(normalizedConfigs, normalizedConfig)
		}
	}

	if len(normalizedConfigs) == 0 {
		c.logger.Debug("controller", fmt.Sprintf("CRD %s does not match any configuration patterns", crd.Name))
		return
	}

	c.logger.Info("controller", fmt.Sprintf("CRD %s matches configuration, starting dynamic informers (selected version: %s)", crd.Name, version))

	// Check if we already have an active informer for this GVR
	if _, exists := c.activeInformers.Load(gvrString); exists {
		c.logger.Debug("controller", fmt.Sprintf("Dynamic informer for %s already active, skipping duplicate", gvrString))
		return
	}
	
	// Mark this GVR as having an active informer
	c.activeInformers.Store(gvrString, true)
	
	// Start single unified informer per GVR that handles all namespaces
	c.wg.Add(1)
	go c.startDynamicCRDInformer(crd.Name, gvr, scope, gvrString, normalizedConfigs)
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

// reconcileStartupCRDs performs a reconciliation after CRD watcher sync to handle race conditions
func (c *Controller) reconcileStartupCRDs() error {
	c.logger.Debug("controller", "Starting CRD reconciliation to handle startup race conditions")

	// Re-fetch current CRDs to catch any that were created/modified during startup
	crdList, err := c.client.Dynamic.Resource(schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}).List(c.ctx, metav1.ListOptions{})

	if err != nil {
		return fmt.Errorf("failed to list CRDs during reconciliation: %w", err)
	}

	reconciledCount := 0
	skippedCount := 0

	for _, crdUnstructured := range crdList.Items {
		var crd apiextensionsv1.CustomResourceDefinition
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(crdUnstructured.Object, &crd); err != nil {
			c.logger.Warning("controller", fmt.Sprintf("Failed to convert CRD during reconciliation: %v", err))
			continue
		}

		// Select version for this CRD
		selectedVersion, err := c.selectCRDVersion(&crd)
		if err != nil {
			c.logger.Warning("controller", fmt.Sprintf("Failed to select version for CRD %s during reconciliation: %v", crd.Name, err))
			continue
		}

		gvrString := fmt.Sprintf("%s/%s/%s", crd.Spec.Group, selectedVersion.Name, crd.Spec.Names.Plural)

		// Check if this CRD matches any configuration but wasn't discovered initially
		matches := false
		for _, nsConfig := range c.config.Namespaces {
			if _, exists := nsConfig.Resources[gvrString]; exists {
				matches = true
				break
			}
		}

		if !matches {
			continue // CRD doesn't match configuration
		}

		// Check if we already have this in discovered resources
		c.discoveredResourcesMu.RLock()
		_, exists := c.discoveredResources[gvrString]
		c.discoveredResourcesMu.RUnlock()
		if exists {
			c.logger.Debug("controller", fmt.Sprintf("CRD %s already in discovered resources, skipping reconciliation", gvrString))
			skippedCount++
			continue
		}

		// Check if we already have an active informer
		if _, exists := c.cancellers.Load(gvrString); exists {
			c.logger.Debug("controller", fmt.Sprintf("CRD %s already has active informer, skipping reconciliation", gvrString))
			skippedCount++
			continue
		}

		// This CRD matches config but wasn't processed - likely a race condition
		c.logger.Info("controller", fmt.Sprintf("Reconciliation: Found matching CRD %s that was missed during startup, starting informer", crd.Name))
		c.evaluateAndStartCRDInformer(&crd)
		reconciledCount++
	}

	if reconciledCount > 0 {
		c.logger.Info("controller", fmt.Sprintf("Startup reconciliation completed: %d CRDs reconciled, %d skipped", reconciledCount, skippedCount))
	} else {
		c.logger.Debug("controller", fmt.Sprintf("Startup reconciliation completed: no missing CRDs found (%d checked)", skippedCount))
	}

	return nil
}

// startDynamicCRDInformer starts a unified dynamic informer for a CRD
func (c *Controller) startDynamicCRDInformer(crdName string, gvr schema.GroupVersionResource, scope apiextensionsv1.ResourceScope, gvrString string, normalizedConfigs []NormalizedConfig) {
	defer c.wg.Done()
	defer c.activeInformers.Delete(gvrString) // Remove from active tracking when stopped

	// Create generic informer config
	config := InformerConfig{
		GVR:       gvr,
		Scope:     scope,
		GVRString: gvrString,
		Context:   c.ctx,
		Name:      crdName,
		HandlerFunc: func(eventType string, obj *unstructured.Unstructured) {
			c.handleUnifiedNormalizedEvent(eventType, obj, gvrString, normalizedConfigs)
		},
	}
	
	// Create informer using label selector factory (handles lister storage)
	informer, err := c.createLabelSelectorInformer(config, normalizedConfigs)
	if err != nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to create dynamic CRD informer: %v", err))
		return
	}
	
	// Run with consistent logging
	c.runInformerWithLogging(informer, c.ctx, fmt.Sprintf("dynamic CRD informer for %s", crdName))
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
		if err.Error() == "no valid configuration found - must have either 'namespaces' or 'resources' section" {
			c.logger.Info("controller", "No namespace or resource configurations found, starting with default watchers")
			return c.startDefaultInformers()
		}
		return fmt.Errorf("failed to normalize configuration: %w", err)
	}

	c.logger.Info("controller", fmt.Sprintf("Normalized configuration: monitoring %d unique GVRs", len(normalizedGVRs)))

	informerCount := 0

	// Start informers per unique GVR, creating separate informers for each namespace when needed
	for gvrString, normalizedConfigs := range normalizedGVRs {
		c.logger.Info("controller", fmt.Sprintf("Setting up informer for %s (matches %d configuration patterns)", gvrString, len(normalizedConfigs)))

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

		c.logger.Debug("controller", fmt.Sprintf("Resource %s: namespaced=%t, normalized configs=%d",
			gvrString, resourceInfo.Namespaced, len(normalizedConfigs)))

		// Check if we already have an active informer for this GVR
		if _, exists := c.activeInformers.Load(gvrString); exists {
			c.logger.Debug("controller", fmt.Sprintf("Informer for %s already active, skipping duplicate", gvrString))
			continue
		}
		
		// Mark this GVR as having an active informer
		c.activeInformers.Store(gvrString, true)
		
		// Start single unified informer per GVR that handles all namespaces
		c.wg.Add(1)
		go c.startUnifiedNormalizedInformer(gvr, scope, gvrString, normalizedConfigs)
		informerCount++
	}

	c.builtinCount = informerCount
	c.logger.Info("controller", fmt.Sprintf("Started %d config-driven informers (deduplicated)", informerCount))
	return nil
}

// startDefaultInformers starts default informers when no config is provided
func (c *Controller) startDefaultInformers() error {
	c.logger.Info("controller", "Starting default informers")

	// Default watchers for testing
	defaultResources := []struct {
		gvr   schema.GroupVersionResource
		scope apiextensionsv1.ResourceScope
		name  string
	}{
		{
			gvr:   schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
			scope: apiextensionsv1.NamespaceScoped,
			name:  "pods",
		},
	}

	c.builtinCount = len(defaultResources)

	for _, resource := range defaultResources {
		c.wg.Add(1)
		go c.startBuiltinInformer(resource.gvr, resource.scope, resource.name)
	}

	c.logger.Info("controller", fmt.Sprintf("Started %d default informers", len(defaultResources)))
	return nil
}

// startBuiltinInformer starts a single builtin informer in its own goroutine
func (c *Controller) startBuiltinInformer(gvr schema.GroupVersionResource, scope apiextensionsv1.ResourceScope, name string) {
	defer c.wg.Done()
	
	// Create generic informer config
	config := InformerConfig{
		GVR:       gvr,
		Scope:     scope,
		GVRString: name,
		Context:   c.ctx,
		Name:      name,
		HandlerFunc: func(eventType string, obj *unstructured.Unstructured) {
			c.handleBuiltinEvent(eventType, obj, name)
		},
	}
	
	// Create informer using generic factory
	informer, err := c.createGenericInformer(config)
	if err != nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to create builtin informer: %v", err))
		return
	}
	
	// Run with consistent logging
	c.runInformerWithLogging(informer, c.ctx, fmt.Sprintf("builtin informer for %s", name))
}

// startDynamicInformer starts a dynamic informer for a specific CRD
func (c *Controller) startDynamicInformer(crdName, group, version, resource string, scope apiextensionsv1.ResourceScope) {
	defer c.wg.Done()

	// Create child context for this specific informer with cancel function
	ctx, cancel := context.WithCancel(c.ctx)
	gvrString := fmt.Sprintf("%s/%s/%s", group, version, resource)
	c.cancellers.Store(gvrString, cancel)

	defer func() {
		cancel()
		c.cancellers.Delete(gvrString)
		c.logger.Info("controller", fmt.Sprintf("Dynamic informer for %s (GVR: %s) stopped", crdName, gvrString))
	}()

	// Create GVR for this custom resource
	gvr := schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	}

	// Create generic informer config
	config := InformerConfig{
		GVR:       gvr,
		Scope:     scope,
		GVRString: gvrString,
		Context:   ctx,
		Name:      crdName,
		HandlerFunc: func(eventType string, obj *unstructured.Unstructured) {
			c.handleCustomResourceEvent(eventType, obj, crdName)
		},
	}
	
	// Create informer using generic factory
	informer, err := c.createGenericInformer(config)
	if err != nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to create dynamic informer: %v", err))
		return
	}
	
	// Run with consistent logging
	c.runInformerWithLogging(informer, ctx, fmt.Sprintf("dynamic informer for %s", crdName))
}

// handleBuiltinEvent processes events from builtin resource informers
func (c *Controller) handleBuiltinEvent(eventType string, obj *unstructured.Unstructured, resourceType string) {
	name := obj.GetName()
	namespace := obj.GetNamespace()
	uid := obj.GetUID()

	if namespace != "" {
		c.logger.Info("controller", fmt.Sprintf("BUILTIN [%s] %s %s/%s (UID: %s)",
			eventType, resourceType, namespace, name, uid))
	} else {
		c.logger.Info("controller", fmt.Sprintf("BUILTIN [%s] %s %s (UID: %s)",
			eventType, resourceType, name, uid))
	}

	// Future: Add sophisticated event processing, correlation, file output, etc.
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
func (c *Controller) GetActiveInformers() (builtin int, dynamic int) {
	builtin = c.builtinCount

	// Count dynamic informers
	dynamic = 0
	c.cancellers.Range(func(key, value interface{}) bool {
		dynamic++
		return true
	})

	c.logger.Debug("controller", fmt.Sprintf("Active informers: %d builtin, %d dynamic", builtin, dynamic))
	return builtin, dynamic
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

	// Wait for all goroutines to finish with timeout protection
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	// Wait with timeout to prevent hanging
	select {
	case <-done:
		c.logger.Info("controller", "All informers and workers stopped gracefully")
	case <-time.After(25 * time.Second):
		c.logger.Warning("controller", "Timeout waiting for informers and workers to stop, some may still be running")
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

	// Get lister for this GVR
	listerInterface, exists := c.listers.Load(workItem.GVRString)
	if !exists {
		return fmt.Errorf("no lister found for GVR %s (key: %s)", workItem.GVRString, workItem.Key)
	}

	lister, ok := listerInterface.(cache.GenericLister)
	if !ok {
		return fmt.Errorf("invalid lister type for GVR %s", workItem.GVRString)
	}

	obj, err := lister.Get(workItem.Key)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			// The object was deleted. Log CONFIG message and call OnMatched handlers.
			c.logger.Info("controller", fmt.Sprintf("CONFIG [DELETED] %s %s", workItem.GVRString, workItem.Key))
			
			// Parse the key to get namespace and name for JSON event
			namespace, name, keyErr := cache.SplitMetaNamespaceKey(workItem.Key)
			if keyErr != nil {
				// For cluster-scoped resources, key is just the name
				name = workItem.Key
				namespace = ""
			}
			
			// Log JSON event for DELETE - no involvedObject data available
			c.logJSONEvent("DELETED", workItem.GVRString, namespace, name, "", nil, nil)
			
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
		if len(config.NamespacePatterns) == 0 {
			// No namespace patterns means match all namespaces
			namespaceMatches = true
		} else {
			// Check if resource namespace matches any of the configured patterns
			for _, pattern := range config.NamespacePatterns {
				if pattern == "" {
					// Empty pattern means all namespaces
					namespaceMatches = true
					break
				} else if pattern == resourceNamespace {
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
			c.logger.Info("controller", fmt.Sprintf("CONFIG [%s] %s %s/%s (UID: %s, pattern: %s)",
				eventType, gvrString, resourceNamespace, resourceName, resourceUID, config.GVR))
		} else {
			c.logger.Info("controller", fmt.Sprintf("CONFIG [%s] %s %s (UID: %s, pattern: %s)",
				eventType, gvrString, resourceName, resourceUID, config.GVR))
		}
		
		// Log JSON event for export
		c.logJSONEvent(eventType, gvrString, resourceNamespace, resourceName, string(resourceUID), obj.GetLabels(), obj)
		
		break // Only process once per object
	}

	return nil
}

// REMOVED: All client-side filtering functions have been eliminated from Faro core

// startUnifiedConfigDrivenInformer starts an informer that handles multiple namespace configurations for the same GVR
func (c *Controller) startUnifiedConfigDrivenInformer(gvr schema.GroupVersionResource, scope apiextensionsv1.ResourceScope, gvrString string, nsConfigs []NamespaceConfig) {
	defer c.wg.Done()
	defer c.activeInformers.Delete(gvrString) // Remove from active tracking when stopped

	// Create generic informer config
	config := InformerConfig{
		GVR:       gvr,
		Scope:     scope,
		GVRString: gvrString,
		Context:   c.ctx,
		Name:      gvrString,
		HandlerFunc: func(eventType string, obj *unstructured.Unstructured) {
			c.handleUnifiedConfigDrivenEvent(eventType, obj, gvrString, nsConfigs)
		},
	}
	
	// Create informer using generic factory
	informer, err := c.createGenericInformer(config)
	if err != nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to create unified config-driven informer: %v", err))
		return
	}
	
	// Run with consistent logging - include label selector info if present
	description := fmt.Sprintf("unified config-driven informer for %s", gvrString)
	if len(nsConfigs) > 0 {
		// Look for label selector in the resource details
		for _, nsConfig := range nsConfigs {
			if resourceDetails, exists := nsConfig.Resources[gvrString]; exists && resourceDetails.LabelSelector != "" {
				description = fmt.Sprintf("unified config-driven informer for %s (label selector: %s)", gvrString, resourceDetails.LabelSelector)
				break
			}
		}
	}
	c.runInformerWithLogging(informer, c.ctx, description)
}

// handleConfigDrivenEvent processes events with NO client-side filtering
func (c *Controller) handleConfigDrivenEvent(eventType string, obj *unstructured.Unstructured, gvrString string, nsConfig NamespaceConfig) {
	resourceName := obj.GetName()
	resourceNamespace := obj.GetNamespace()
	resourceUID := obj.GetUID()

	// NO CLIENT-SIDE FILTERING - Process all events that reach here
	// Server-side filtering and application logic handle all filtering
	
	// Log all events without filtering
	if resourceNamespace != "" {
		c.logger.Info("controller", fmt.Sprintf("CONFIG [%s] %s %s/%s (UID: %s)",
			eventType, gvrString, resourceNamespace, resourceName, resourceUID))
	} else {
		c.logger.Info("controller", fmt.Sprintf("CONFIG [%s] %s %s (UID: %s)",
			eventType, gvrString, resourceName, resourceUID))
	}
}

// handleUnifiedConfigDrivenEvent processes events with multiple config-based filtering
func (c *Controller) handleUnifiedConfigDrivenEvent(eventType string, obj *unstructured.Unstructured, gvrString string, nsConfigs []NamespaceConfig) {
	resourceName := obj.GetName()
	resourceNamespace := obj.GetNamespace()
	resourceUID := obj.GetUID()

	// NO CLIENT-SIDE FILTERING - Process all events that reach here
	// Server-side filtering and application logic handle all filtering

	// Log all events without filtering
		if resourceNamespace != "" {
		c.logger.Info("controller", fmt.Sprintf("CONFIG [%s] %s %s/%s (UID: %s)",
			eventType, gvrString, resourceNamespace, resourceName, resourceUID))
	} else {
		c.logger.Info("controller", fmt.Sprintf("CONFIG [%s] %s %s (UID: %s)",
			eventType, gvrString, resourceName, resourceUID))
	}
}


// startUnifiedNormalizedInformer starts an informer that handles multiple normalized configurations for the same GVR
func (c *Controller) startUnifiedNormalizedInformer(gvr schema.GroupVersionResource, scope apiextensionsv1.ResourceScope, gvrString string, normalizedConfigs []NormalizedConfig) {
	defer c.wg.Done()
	defer c.activeInformers.Delete(gvrString) // Remove from active tracking when stopped

	// Create generic informer config
	config := InformerConfig{
		GVR:       gvr,
		Scope:     scope,
		GVRString: gvrString,
		Context:   c.ctx,
		Name:      gvrString,
		HandlerFunc: func(eventType string, obj *unstructured.Unstructured) {
			c.handleUnifiedNormalizedEvent(eventType, obj, gvrString, normalizedConfigs)
		},
	}
	
	// Create informer using label selector factory (handles lister storage)
	informer, err := c.createLabelSelectorInformer(config, normalizedConfigs)
	if err != nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to create unified normalized informer: %v", err))
		return
	}
	
	// Run with consistent logging - include label selector info if present
	description := fmt.Sprintf("unified config-driven informer for %s", gvrString)
	if len(normalizedConfigs) > 0 && normalizedConfigs[0].LabelSelector != "" {
		description = fmt.Sprintf("unified config-driven informer for %s (label selector: %s)", gvrString, normalizedConfigs[0].LabelSelector)
	}
	c.runInformerWithLogging(informer, c.ctx, description)
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

	c.logger.Debug("controller", fmt.Sprintf("Queueing %s event for %s %s", eventType, gvrString, key))
	c.workQueue.Add(workItem)
}
