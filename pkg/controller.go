package faro

import (
	"context"
	"fmt"
	"regexp"
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
	discoveredResources map[string]*ResourceInfo // map[GVR] -> ResourceInfo

	// Informer lifecycle management - using GVR string as consistent key
	cancellers      sync.Map // map[string]context.CancelFunc for informer shutdown
	activeInformers sync.Map // map[string]bool for tracking active informers by GVR
	listers         sync.Map // map[string]cache.GenericLister for object retrieval

	// Track builtin informer count
	builtinCount int
}

// NewController creates a new informer-based controller
func NewController(client *KubernetesClient, logger *Logger, config *Config) *Controller {
	ctx, cancel := context.WithCancel(context.Background())

	return &Controller{
		client:              client,
		logger:              logger,
		config:              config,
		ctx:                 ctx,
		cancel:              cancel,
		workQueue:           workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "faro-controller"),
		workers:             3, // Start with 3 worker goroutines
		discoveredResources: make(map[string]*ResourceInfo),
	}
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

	c.logger.Info("controller", fmt.Sprintf("Discovery completed: %d resources found", len(c.discoveredResources)))
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
		if _, exists := c.discoveredResources[gvrKey]; !exists {
			c.discoveredResources[gvrKey] = resourceInfo
			c.logger.Debug("controller", fmt.Sprintf("Discovered resource: %s (Kind: %s, Namespaced: %t)",
				gvrKey, resource.Kind, resource.Namespaced))
		}
	}

	return nil
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
			if unstructuredObj, ok := obj.(*unstructured.Unstructured); ok {
				c.handleCRDDeleted(unstructuredObj)
			} else {
				c.logger.Error("controller", "Received unexpected object type in CRD DeleteFunc")
			}
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
	if _, alreadyDiscovered := c.discoveredResources[gvrString]; alreadyDiscovered {
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

// evaluateAndStartCRDInformer checks if a CRD matches configuration and starts an informer
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

	// Check if this GVR matches any configured namespace patterns
	for _, nsConfig := range c.config.Namespaces {
		if _, exists := nsConfig.Resources[gvrString]; exists {
			c.logger.Info("controller", fmt.Sprintf("CRD %s matches configuration, starting dynamic informer (selected version: %s)", crd.Name, version))

			// Add to discovered resources
			resourceInfo := &ResourceInfo{
				Group:      group,
				Version:    version,
				Resource:   resource,
				Kind:       crd.Spec.Names.Kind,
				Namespaced: crd.Spec.Scope == apiextensionsv1.NamespaceScoped,
			}
			c.discoveredResources[gvrString] = resourceInfo

			// Start informer for this CRD
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

			// Start the dynamic informer
			c.wg.Add(1)
			go c.startDynamicCRDInformer(crd.Name, gvr, scope, gvrString, nsConfig)

			break // Only start one informer per CRD
		}
	}
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
		if _, exists := c.discoveredResources[gvrString]; exists {
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

// startDynamicCRDInformer starts a dynamic informer for a specific CRD
func (c *Controller) startDynamicCRDInformer(crdName string, gvr schema.GroupVersionResource, scope apiextensionsv1.ResourceScope, gvrString string, nsConfig NamespaceConfig) {
	defer c.wg.Done()

	c.logger.Info("controller", fmt.Sprintf("Starting dynamic informer for CRD %s (%s)", crdName, gvrString))

	// Create context for this specific informer
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()

	// Store the cancel function for graceful shutdown using GVR string as key
	gvrKey := gvr.String()
	c.cancellers.Store(gvrKey, cancel)
	defer c.cancellers.Delete(gvrKey)

	var namespace string
	if scope == apiextensionsv1.NamespaceScoped {
		namespace = "" // Watch all namespaces, filter in event handler
	}

	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		c.client.Dynamic, 10*time.Minute, namespace, nil)

	informer := factory.ForResource(gvr).Informer()

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.handleConfigDrivenEvent("ADDED", obj.(*unstructured.Unstructured), gvrString, nsConfig)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.handleConfigDrivenEvent("UPDATED", newObj.(*unstructured.Unstructured), gvrString, nsConfig)
		},
		DeleteFunc: func(obj interface{}) {
			c.handleConfigDrivenEvent("DELETED", obj.(*unstructured.Unstructured), gvrString, nsConfig)
		},
	})

	c.logger.Info("controller", fmt.Sprintf("Running dynamic informer for CRD %s", crdName))
	informer.Run(ctx.Done())
	c.logger.Info("controller", fmt.Sprintf("Dynamic informer for CRD %s stopped", crdName))
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

	if _, exists := c.discoveredResources[gvrString]; exists {
		delete(c.discoveredResources, gvrString)
		c.logger.Debug("controller", fmt.Sprintf("Removed %s from discovered resources", gvrString))
	} else {
		c.logger.Debug("controller", fmt.Sprintf("Resource %s not found in discovered resources (already cleaned up)", gvrString))
	}
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

	// Start one informer per unique GVR with all matching normalized configs
	for gvrString, normalizedConfigs := range normalizedGVRs {
		c.logger.Info("controller", fmt.Sprintf("Setting up informer for %s (matches %d configuration patterns)", gvrString, len(normalizedConfigs)))

		// Look up resource info from discovery
		resourceInfo, found := c.discoveredResources[gvrString]
		if !found {
			c.logger.Warning("controller", fmt.Sprintf("Resource %s not found in discovery results, skipping", gvrString))
			continue
		}

		// Check if we already have an active informer for this GVR
		if _, exists := c.activeInformers.Load(gvrString); exists {
			c.logger.Debug("controller", fmt.Sprintf("Informer for %s already active, skipping duplicate", gvrString))
			continue
		}

		// Mark this GVR as having an active informer
		c.activeInformers.Store(gvrString, true)

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

		// Start an informer that handles all matching normalized configurations
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

	c.logger.Info("controller", fmt.Sprintf("Starting builtin informer for %s", name))

	// Determine namespace scope
	var namespace string
	if scope == apiextensionsv1.NamespaceScoped {
		namespace = "" // All namespaces
	}

	// Create dynamic informer factory
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		c.client.Dynamic, 10*time.Minute, namespace, nil)

	informer := factory.ForResource(gvr).Informer()

	// Add event handlers
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.handleBuiltinEvent("ADDED", obj.(*unstructured.Unstructured), name)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.handleBuiltinEvent("UPDATED", newObj.(*unstructured.Unstructured), name)
		},
		DeleteFunc: func(obj interface{}) {
			c.handleBuiltinEvent("DELETED", obj.(*unstructured.Unstructured), name)
		},
	})

	// Start the informer
	c.logger.Info("controller", fmt.Sprintf("Running builtin informer for %s", name))
	informer.Run(c.ctx.Done())
	c.logger.Info("controller", fmt.Sprintf("Builtin informer for %s stopped", name))
}

// startDynamicInformer starts a dynamic informer for a specific CRD
func (c *Controller) startDynamicInformer(crdName, group, version, resource string, scope apiextensionsv1.ResourceScope) {
	defer c.wg.Done()

	c.logger.Info("controller", fmt.Sprintf("Starting dynamic informer for CRD: %s (%s/%s/%s)",
		crdName, group, version, resource))

	// Create child context for this specific informer with cancel function
	ctx, cancel := context.WithCancel(c.ctx)
	gvrString := fmt.Sprintf("%s/%s/%s", group, version, resource)
	c.cancellers.Store(gvrString, cancel)

	defer func() {
		cancel()
		c.cancellers.Delete(gvrString)
		c.logger.Info("controller", fmt.Sprintf("Dynamic informer for %s (GVR: %s) stopped", crdName, gvrString))
	}()

	// Determine namespace scope
	var namespace string
	if scope == apiextensionsv1.NamespaceScoped {
		namespace = "" // All namespaces
	}

	// Create GVR for this custom resource
	gvr := schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	}

	// Create dynamic informer factory
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		c.client.Dynamic, 10*time.Minute, namespace, nil)

	informer := factory.ForResource(gvr).Informer()

	// Add event handlers for custom resources
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.handleCustomResourceEvent("ADDED", obj.(*unstructured.Unstructured), crdName)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.handleCustomResourceEvent("UPDATED", newObj.(*unstructured.Unstructured), crdName)
		},
		DeleteFunc: func(obj interface{}) {
			c.handleCustomResourceEvent("DELETED", obj.(*unstructured.Unstructured), crdName)
		},
	})

	// Start the informer
	c.logger.Info("controller", fmt.Sprintf("Running dynamic informer for %s", crdName))
	informer.Run(ctx.Done())
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
	// Step 1: Parse the key to get namespace and name.
	namespace, name, err := cache.SplitMetaNamespaceKey(workItem.Key)
	if err != nil {
		c.logger.Warning("controller", fmt.Sprintf("Invalid resource key: %s", workItem.Key))
		return nil
	}

	// Step 2: Check if the key matches any of the relevant configurations for this GVR.
	isMatch := false
	for _, config := range workItem.Configs {
		if c.matchesConfigByKey(namespace, name, config) {
			isMatch = true
			break
		}
	}

	if !isMatch {
		// This object does not match our filters, so we don't care about it,
		// whether it was added, updated, or deleted.
		c.logger.Debug("controller", fmt.Sprintf("Skipping event for %s %s as it does not match any config", workItem.GVRString, workItem.Key))
		return nil
	}

	// At this point, we know the object is one we are configured to watch.

	listerInterface, exists := c.listers.Load(workItem.GVRString)
	if !exists {
		return fmt.Errorf("no lister found for GVR %s", workItem.GVRString)
	}

	lister, ok := listerInterface.(cache.GenericLister)
	if !ok {
		return fmt.Errorf("invalid lister type for GVR %s", workItem.GVRString)
	}

	obj, err := lister.Get(workItem.Key)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			// The object was deleted. Now we log it with the correct "CONFIG" prefix.
			c.logger.Info("controller", fmt.Sprintf("CONFIG [DELETED] %s %s", workItem.GVRString, workItem.Key))
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

	// Apply filtering logic from configs
	for _, config := range configs {
		// Check if this object matches the config's patterns
		if c.matchesConfig(obj, config) {
			// Log the matched event
			if resourceNamespace != "" {
				c.logger.Info("controller", fmt.Sprintf("CONFIG [%s] %s %s/%s (UID: %s, pattern: %s)",
					eventType, gvrString, resourceNamespace, resourceName, resourceUID, config.GVR))
			} else {
				c.logger.Info("controller", fmt.Sprintf("CONFIG [%s] %s %s (UID: %s, pattern: %s)",
					eventType, gvrString, resourceName, resourceUID, config.GVR))
			}
			break // Only log once per object
		}
	}

	return nil
}

// matchesConfig checks if an object matches a normalized config's patterns
func (c *Controller) matchesConfig(obj *unstructured.Unstructured, config NormalizedConfig) bool {
	resourceName := obj.GetName()
	resourceNamespace := obj.GetNamespace()

	return c.matchesConfigByKey(resourceNamespace, resourceName, config)
}

// matchesConfigByKey checks if a resource key (namespace/name) matches a normalized config.
func (c *Controller) matchesConfigByKey(namespace, name string, config NormalizedConfig) bool {
	// Check name pattern
	if config.ResourceDetails.NamePattern != "" {
		matched, err := regexp.MatchString(config.ResourceDetails.NamePattern, name)
		if err != nil || !matched {
			return false
		}
	}

	// Check namespace patterns (only for namespaced resources)
	if namespace != "" && len(config.NamespacePatterns) > 0 {
		namespaceMatched := false
		for _, nsPattern := range config.NamespacePatterns {
			// Handle cluster-scoped resources where the pattern might be empty
			if nsPattern == "" && namespace == "" {
				namespaceMatched = true
				break
			}
			if matched, err := regexp.MatchString(nsPattern, namespace); err == nil && matched {
				namespaceMatched = true
				break
			}
		}
		if !namespaceMatched {
			return false
		}
	}

	return true
}

// startUnifiedConfigDrivenInformer starts an informer that handles multiple namespace configurations for the same GVR
func (c *Controller) startUnifiedConfigDrivenInformer(gvr schema.GroupVersionResource, scope apiextensionsv1.ResourceScope, gvrString string, nsConfigs []NamespaceConfig) {
	defer c.wg.Done()
	defer c.activeInformers.Delete(gvrString) // Remove from active tracking when stopped

	c.logger.Info("controller", fmt.Sprintf("Starting unified config-driven informer for %s", gvrString))

	// For namespace-scoped resources, we need to watch all namespaces and filter
	// For cluster-scoped resources, watch globally
	var namespace string
	if scope == apiextensionsv1.NamespaceScoped {
		namespace = "" // Watch all namespaces, filter in event handler
	}

	// Create dynamic informer factory with error handling
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		c.client.Dynamic, 10*time.Minute, namespace, nil)

	// Get informer with error handling
	informer := factory.ForResource(gvr).Informer()

	// Validate that the informer was created successfully
	if informer == nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to create informer for GVR %s", gvrString))
		return
	}

	// Add event handlers with unified config-based filtering and error handling
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if unstructuredObj, ok := obj.(*unstructured.Unstructured); ok {
				c.handleUnifiedConfigDrivenEvent("ADDED", unstructuredObj, gvrString, nsConfigs)
			} else {
				c.logger.Error("controller", fmt.Sprintf("Received unexpected object type in AddFunc for %s", gvrString))
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if unstructuredObj, ok := newObj.(*unstructured.Unstructured); ok {
				c.handleUnifiedConfigDrivenEvent("UPDATED", unstructuredObj, gvrString, nsConfigs)
			} else {
				c.logger.Error("controller", fmt.Sprintf("Received unexpected object type in UpdateFunc for %s", gvrString))
			}
		},
		DeleteFunc: func(obj interface{}) {
			if unstructuredObj, ok := obj.(*unstructured.Unstructured); ok {
				c.handleUnifiedConfigDrivenEvent("DELETED", unstructuredObj, gvrString, nsConfigs)
			} else {
				c.logger.Error("controller", fmt.Sprintf("Received unexpected object type in DeleteFunc for %s", gvrString))
			}
		},
	})

	// Start the informer
	c.logger.Info("controller", fmt.Sprintf("Running unified config-driven informer for %s", gvrString))
	informer.Run(c.ctx.Done())
	c.logger.Info("controller", fmt.Sprintf("Unified config-driven informer for %s stopped", gvrString))
}

// handleConfigDrivenEvent processes events with config-based filtering
func (c *Controller) handleConfigDrivenEvent(eventType string, obj *unstructured.Unstructured, gvrString string, nsConfig NamespaceConfig) {
	resourceName := obj.GetName()
	resourceNamespace := obj.GetNamespace()
	resourceUID := obj.GetUID()

	// For namespace-scoped resources, check if namespace matches pattern
	if resourceNamespace != "" {
		if matched, _ := regexp.MatchString(nsConfig.NamePattern, resourceNamespace); !matched {
			// Namespace doesn't match pattern, skip this event
			c.logger.Debug("controller", fmt.Sprintf("Skipping %s %s/%s - namespace '%s' doesn't match pattern '%s'",
				gvrString, resourceNamespace, resourceName, resourceNamespace, nsConfig.NamePattern))
			return
		}
	}

	// Check if resource name matches the configured pattern for this GVR
	if !nsConfig.MatchesResource(gvrString, resourceName) {
		// Resource name doesn't match pattern, skip this event
		resourceConfig := nsConfig.Resources[gvrString]
		c.logger.Debug("controller", fmt.Sprintf("Skipping %s %s/%s - name '%s' doesn't match pattern '%s'",
			gvrString, resourceNamespace, resourceName, resourceName, resourceConfig.NamePattern))
		return
	}

	// Event passed all filters, log it with additional context
	if resourceNamespace != "" {
		c.logger.Info("controller", fmt.Sprintf("CONFIG [%s] %s %s/%s (UID: %s)",
			eventType, gvrString, resourceNamespace, resourceName, resourceUID))

		// Special handling for namespace deletion detection
		if eventType == "DELETED" && gvrString == "v1/namespaces" {
			c.logger.Warning("controller", fmt.Sprintf("NAMESPACE DELETED: %s - all resources in this namespace will be automatically cleaned up", resourceName))
		}
	} else {
		c.logger.Info("controller", fmt.Sprintf("CONFIG [%s] %s %s (UID: %s)",
			eventType, gvrString, resourceName, resourceUID))
	}

	// Future: Add sophisticated event processing, correlation, file output, etc.
}

// handleUnifiedConfigDrivenEvent processes events with multiple config-based filtering
func (c *Controller) handleUnifiedConfigDrivenEvent(eventType string, obj *unstructured.Unstructured, gvrString string, nsConfigs []NamespaceConfig) {
	resourceName := obj.GetName()
	resourceNamespace := obj.GetNamespace()
	resourceUID := obj.GetUID()

	// Check against all namespace configurations to see if this event matches any pattern
	matched := false
	var matchedConfig NamespaceConfig

	for _, nsConfig := range nsConfigs {
		// For namespace-scoped resources, check if namespace matches pattern
		if resourceNamespace != "" {
			nameMatched, err := regexp.MatchString(nsConfig.NamePattern, resourceNamespace)
			if err != nil {
				c.logger.Error("controller", fmt.Sprintf("Invalid regex pattern '%s' for namespace: %v", nsConfig.NamePattern, err))
				continue
			}
			if !nameMatched {
				continue // Namespace doesn't match this pattern
			}
		}

		// Check if resource name matches the configured pattern for this GVR
		if nsConfig.MatchesResource(gvrString, resourceName) {
			matched = true
			matchedConfig = nsConfig
			break // Found a matching configuration
		}
	}

	if !matched {
		// Event doesn't match any configuration, skip with debug log
		c.logger.Debug("controller", fmt.Sprintf("Skipping %s %s/%s - doesn't match any configured patterns",
			gvrString, resourceNamespace, resourceName))
		return
	}

	// Event passed filters for at least one configuration, log it
	if resourceNamespace != "" {
		c.logger.Info("controller", fmt.Sprintf("CONFIG [%s] %s %s/%s (UID: %s, pattern: %s)",
			eventType, gvrString, resourceNamespace, resourceName, resourceUID, matchedConfig.NamePattern))

		// Special handling for namespace deletion detection
		if eventType == "DELETED" && gvrString == "v1/namespaces" {
			c.logger.Warning("controller", fmt.Sprintf("NAMESPACE DELETED: %s - all resources in this namespace will be automatically cleaned up", resourceName))
		}
	} else {
		c.logger.Info("controller", fmt.Sprintf("CONFIG [%s] %s %s (UID: %s, pattern: %s)",
			eventType, gvrString, resourceName, resourceUID, matchedConfig.NamePattern))
	}

	// Future: Add sophisticated event processing, correlation, file output, etc.
}

// startUnifiedNormalizedInformer starts an informer that handles multiple normalized configurations for the same GVR
func (c *Controller) startUnifiedNormalizedInformer(gvr schema.GroupVersionResource, scope apiextensionsv1.ResourceScope, gvrString string, normalizedConfigs []NormalizedConfig) {
	defer c.wg.Done()
	defer c.activeInformers.Delete(gvrString) // Remove from active tracking when stopped

	c.logger.Info("controller", fmt.Sprintf("Starting unified config-driven informer for %s", gvrString))

	// For namespace-scoped resources, we need to watch all namespaces and filter
	// For cluster-scoped resources, watch globally
	var namespace string
	if scope == apiextensionsv1.NamespaceScoped {
		namespace = "" // Watch all namespaces, filter in event handler
	}

	// Determine the label selector to use for this GVR
	// For simplicity, we'll use the first non-empty label selector found
	// In a more sophisticated implementation, you might want to merge selectors
	var labelSelector string
	for _, config := range normalizedConfigs {
		if config.LabelSelector != "" {
			labelSelector = config.LabelSelector
			break
		}
	}

	// Create a tweakListOptions function to apply the label selector
	tweakListOptions := func(options *metav1.ListOptions) {
		if labelSelector != "" {
			options.LabelSelector = labelSelector
			c.logger.Debug("controller", fmt.Sprintf("Applying label selector '%s' to informer for %s", labelSelector, gvrString))
		}
	}

	// Create dynamic informer factory with label selector filtering
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		c.client.Dynamic, 10*time.Minute, namespace, tweakListOptions)

	// Get informer with error handling
	informer := factory.ForResource(gvr).Informer()

	// Validate that the informer was created successfully
	if informer == nil {
		c.logger.Error("controller", fmt.Sprintf("Failed to create informer for GVR %s", gvrString))
		return
	}

	// Store the lister for later retrieval by workers
	lister := factory.ForResource(gvr).Lister()
	c.listers.Store(gvrString, lister)

	// Add event handlers with unified normalized config-based filtering and error handling
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if unstructuredObj, ok := obj.(*unstructured.Unstructured); ok {
				c.handleUnifiedNormalizedEvent("ADDED", unstructuredObj, gvrString, normalizedConfigs)
			} else {
				c.logger.Error("controller", fmt.Sprintf("Received unexpected object type in AddFunc for %s", gvrString))
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if unstructuredObj, ok := newObj.(*unstructured.Unstructured); ok {
				c.handleUnifiedNormalizedEvent("UPDATED", unstructuredObj, gvrString, normalizedConfigs)
			} else {
				c.logger.Error("controller", fmt.Sprintf("Received unexpected object type in UpdateFunc for %s", gvrString))
			}
		},
		DeleteFunc: func(obj interface{}) {
			if unstructuredObj, ok := obj.(*unstructured.Unstructured); ok {
				c.handleUnifiedNormalizedEvent("DELETED", unstructuredObj, gvrString, normalizedConfigs)
			} else {
				c.logger.Error("controller", fmt.Sprintf("Received unexpected object type in DeleteFunc for %s", gvrString))
			}
		},
	})

	// Start the informer
	c.logger.Info("controller", fmt.Sprintf("Running unified config-driven informer for %s", gvrString))
	informer.Run(c.ctx.Done())
	c.logger.Info("controller", fmt.Sprintf("Unified config-driven informer for %s stopped", gvrString))
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
