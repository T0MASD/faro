package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	faro "github.com/T0MASD/faro/pkg"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// WorkloadMonitor handles dynamic discovery and monitoring of workloads
type WorkloadMonitor struct {
	client                   *faro.KubernetesClient
	logger                   *faro.Logger
	discoveryController      *faro.Controller         // Cluster controller for namespace discovery
	unifiedController        *faro.Controller         // Single unified controller for all workload resources
	mu                       sync.RWMutex
	
	// Configuration
	detectionLabel           string        // Label key to look for (e.g., "app.kubernetes.io/name")
	workloadNamePattern      *regexp.Regexp // Pattern to match workload names
	workloadIDPattern        string        // Pattern to extract workload ID from namespace names (e.g., "ocm-staging-(.+)")
	logDir                   string
	logLevel                 string        // Log level for controllers
	cmdClusterGVRs           []string      // Command-line cluster-scoped GVRs
	cmdNamespaceGVRs         []string      // Command-line namespace-scoped GVRs (per-namespace informers)
	
	// Context information
	clusterName              string        // Name/identifier of the cluster being monitored
	commandLine              string        // Full command line used to start the monitor
	
	// State tracking
	detectedWorkloads        map[string][]string      // workloadID -> namespaces
	workloadIDToWorkloadName map[string]string
	
	// Dynamic GVR discovery tracking
	discoveredGVRs           map[string]map[string]bool // namespace -> gvr -> true (tracks which GVRs are monitored per namespace)
	initialGVRs              map[string]bool            // gvr -> true (tracks initially configured GVRs)
	dynamicGVRs              map[string]bool            // gvr -> true (tracks dynamically discovered GVRs)
}

// StructuredLogEntry represents a structured log entry for Kubernetes resources
type StructuredLogEntry struct {
	Timestamp time.Time       `json:"timestamp"`
	Level     string          `json:"level"`
	Message   string          `json:"message"`
	Workload  WorkloadContext `json:"workload"`
}

// WorkloadContext contains workload-specific metadata
type WorkloadContext struct {
	WorkloadID   string            `json:"workload_id"`
	WorkloadName string            `json:"workload_name,omitempty"`
	Namespace    string            `json:"namespace,omitempty"`
	ResourceType string            `json:"resource_type"`
	ResourceName string            `json:"resource_name"`
	Action       string            `json:"action"`
	UID          string            `json:"uid,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

// UnifiedWorkloadHandler handles events for all workloads in the unified controller
// NOTE: This is now used only for dynamic GVR discovery, not annotation injection
type UnifiedWorkloadHandler struct {
	Monitor *WorkloadMonitor
}

func (u *UnifiedWorkloadHandler) OnMatched(event faro.MatchedEvent) error {
	// Extract namespace from event key (format: "namespace/name" or just "name" for cluster-scoped)
	namespace := ""
	if strings.Contains(event.Key, "/") {
		parts := strings.SplitN(event.Key, "/", 2)
		namespace = parts[0]
	}
	
	// For cluster-scoped resources, skip workload identification
	if namespace == "" {
		return nil
	}
	
	// Determine which workload this event belongs to based on namespace
	workloadID, workloadName := u.Monitor.identifyWorkloadFromNamespace(namespace)
	if workloadID == "" {
		// Event from namespace not associated with any detected workload, skip
		return nil
	}
	
	// Handle dynamic GVR discovery from v1/events
	if event.GVR == "v1/events" {
		namespaces := u.Monitor.getWorkloadNamespaces(workloadID)
		u.Monitor.handleDynamicGVRDiscovery(event, workloadID, namespaces)
	}
	
	return u.Monitor.logResourceEvent(event, workloadID, workloadName)
}

// WorkloadJSONMiddleware implements JSONMiddleware to add workload annotations before JSON logging
type WorkloadJSONMiddleware struct {
	Monitor *WorkloadMonitor
}

func (w *WorkloadJSONMiddleware) ProcessBeforeJSON(eventType, gvr, namespace, name, uid string, obj *unstructured.Unstructured) (*unstructured.Unstructured, bool) {
	// Only process namespaced resources
	if namespace == "" || obj == nil {
		return obj, true
	}
	
	// Determine which workload this event belongs to based on namespace
	workloadID, workloadName := w.Monitor.identifyWorkloadFromNamespace(namespace)
	if workloadID == "" {
		// Event from namespace not associated with any detected workload, skip annotation injection
		return obj, true
	}
	
	w.Monitor.logger.Debug("workload-middleware", 
		"["+w.Monitor.clusterName+"] 🔧 Adding workload annotations to "+eventType+" "+gvr+" "+namespace+"/"+name+" (workload: "+workloadID+")")
	
	// Create a deep copy and add workload annotations
	objCopy := obj.DeepCopy()
	
	if objCopy.GetAnnotations() == nil {
		objCopy.SetAnnotations(make(map[string]string))
	}
	annotations := objCopy.GetAnnotations()
	annotations["faro.workload.id"] = workloadID
	annotations["faro.workload.name"] = workloadName
	objCopy.SetAnnotations(annotations)
	
	return objCopy, true
}

// DeletedResourceMiddleware implements JSONMiddleware to preserve UUIDs for DELETE events
type DeletedResourceMiddleware struct {
	Monitor *WorkloadMonitor
}

func (d *DeletedResourceMiddleware) ProcessBeforeJSON(eventType, gvr, namespace, name, uid string, obj *unstructured.Unstructured) (*unstructured.Unstructured, bool) {
	if eventType == "ADDED" || eventType == "UPDATED" {
		// Store resource info for potential future DELETE events
		if obj != nil && uid != "" && namespace != "" && name != "" {
			// We could implement a cache here, but for now we'll rely on Faro's built-in deleted resource tracking
			d.Monitor.logger.Debug("deleted-resource-tracking", 
				"["+d.Monitor.clusterName+"] 📝 Storing resource info for potential DELETE: "+gvr+" "+namespace+"/"+name+" (UID: "+uid+")")
		}
	}
	
	// For DELETE events, the middleware system will automatically use stored resource info if available
	return obj, true
}

// WorkloadResourceHandler handles events for a specific workload's resources (DEPRECATED - kept for compatibility)
type WorkloadResourceHandler struct {
	WorkloadID   string
	WorkloadName string
	Namespaces   []string
	Monitor      *WorkloadMonitor
}

func (w *WorkloadResourceHandler) OnMatched(event faro.MatchedEvent) error {
	// Handle dynamic GVR discovery from v1/events
	if event.GVR == "v1/events" {
		w.Monitor.handleDynamicGVRDiscovery(event, w.WorkloadID, w.Namespaces)
	}
	
	return w.Monitor.logResourceEvent(event, w.WorkloadID, w.WorkloadName)
}

// OnMatched handles namespace detection for workload discovery
func (w *WorkloadMonitor) OnMatched(event faro.MatchedEvent) error {
	// Only handle namespace events for workload detection
	if event.GVR == "v1/namespaces" && event.EventType == "ADDED" {
		return w.handleNamespaceDetection(event)
	}
	return nil
}


// logResourceEvent creates and logs a structured resource event
func (w *WorkloadMonitor) logResourceEvent(event faro.MatchedEvent, workloadID, workloadName string) error {
	namespace := event.Object.GetNamespace()
	resourceName := event.Object.GetName()
	
	// Optimized logging: single pattern with optional related GVR for v1/events
	logMessage := "[" + workloadID + "] " + event.EventType + " " + event.GVR + " " + namespace + "/" + resourceName
	
	// Add related GVR for v1/events
	if event.GVR == "v1/events" {
		if involvedObj, ok := event.Object.Object["involvedObject"].(map[string]interface{}); ok {
			if relatedGVR := w.extractGVRFromInvolvedObject(involvedObj); relatedGVR != "" {
				logMessage += " → " + relatedGVR
			}
		}
	}
	
	// Removed redundant workload-handler logging - already covered by Faro core [controller] CONFIG logs
	
	// Keep detailed JSON logging for JSON export (but not stdout)
	uid := event.Object.GetUID()
	labels := event.Object.GetLabels()
	
	entry := StructuredLogEntry{
		Timestamp: event.Timestamp.UTC(),
		Level:     "info",
		Message:   "Kubernetes resource " + strings.ToLower(event.EventType),
		Workload: WorkloadContext{
			WorkloadID:   workloadID,
			WorkloadName: workloadName,
			Namespace:    namespace,
			ResourceType: event.GVR,
			ResourceName: resourceName,
			Action:       event.EventType,
			UID:          string(uid),
			Labels:       labels,
		},
	}
	
	// Marshal to JSON for file export (not logged to stdout)
	jsonData, err := json.Marshal(entry)
	if err != nil {
		w.logger.Warning("workload-handler", "Failed to marshal log entry: "+err.Error())
		return err
	}
	
	// Log detailed JSON to debug level only (not stdout)
	w.logger.Debug("workload-handler-json", string(jsonData))
	return nil
}

// handleNamespaceDetection processes namespace events for workload discovery
func (w *WorkloadMonitor) handleNamespaceDetection(event faro.MatchedEvent) error {
	namespaceName := event.Object.GetName()
	labels := event.Object.GetLabels()
	
	// Check if namespace has the detection label
	workloadName, exists := labels[w.detectionLabel]
	if !exists {
		// Skip namespaces without detection label (server-side filtering should prevent this)
		w.logger.Debug("workload-detection", "["+w.clusterName+"] ⚠️  Namespace "+namespaceName+" has no detection label (unexpected with server-side filtering)")
		return nil
	}
	
	// Check if workload name matches our pattern
	if !w.workloadNamePattern.MatchString(workloadName) {
		return nil // Skip non-matching workload names
	}
	
	w.logger.Info("workload-detection", "["+w.clusterName+"] 🔍 MATCHED NAMESPACE: "+namespaceName+" (workload: "+workloadName+")")
	
	// Extract workload ID from namespace name or use a simple approach
	workloadID := w.extractWorkloadID(namespaceName, workloadName)
	
	w.logger.Info("workload-detection", "["+w.clusterName+"] 🎯 DETECTED WORKLOAD: "+workloadID+" (name: "+workloadName+")")
	
	// After detecting a workload from the main namespace, discover all related namespaces
	// that belong to the same workload by name pattern matching
	// Add a small delay to allow other namespaces to propagate to the API server
	time.Sleep(2 * time.Second)
	workloadNamespaces := w.discoverWorkloadNamespaces(workloadID, workloadName)
	
	w.mu.Lock()
	isNewWorkload := w.detectedWorkloads[workloadID] == nil
	previousNamespaces := w.detectedWorkloads[workloadID]
	
	// Add this namespace to the workload (may be additional namespace for existing workload)
	if isNewWorkload {
		w.detectedWorkloads[workloadID] = workloadNamespaces
		w.workloadIDToWorkloadName[workloadID] = workloadName
	} else {
		// Add to existing workload if not already present
		found := false
		for _, ns := range previousNamespaces {
			if ns == namespaceName {
				found = true
				break
			}
		}
		if !found {
			w.detectedWorkloads[workloadID] = append(previousNamespaces, namespaceName)
		}
	}
	w.mu.Unlock()
	
	if isNewWorkload {
		w.logger.Info("workload-detection", "["+w.clusterName+"] 🚀 New workload detected: "+workloadID+" with namespaces: "+fmt.Sprintf("%v", workloadNamespaces))
		w.addWorkloadToUnifiedController(workloadID, workloadName, workloadNamespaces)
	} else if len(workloadNamespaces) > len(previousNamespaces) {
		w.logger.Info("workload-detection", "["+w.clusterName+"] 🔄 Workload "+workloadID+" updated with more namespaces: "+fmt.Sprintf("%v", workloadNamespaces))
		w.addWorkloadToUnifiedController(workloadID, workloadName, workloadNamespaces)
	} else {
		w.logger.Info("workload-detection", "["+w.clusterName+"] 🔄 Workload "+workloadID+" already monitored")
	}
	
	return nil
}

// extractWorkloadID extracts a workload ID from namespace name using the workload ID pattern
func (w *WorkloadMonitor) extractWorkloadID(namespaceName, workloadName string) string {
	// Use workload ID pattern as extraction regex (should have capture group)
	// Example: "ocm-staging-(.+)" extracts the suffix from "ocm-staging-XXXXX"
	
	re, err := regexp.Compile(w.workloadIDPattern)
	if err != nil {
		w.logger.Error("workload-extraction", "Invalid workload ID pattern regex: "+err.Error())
		return namespaceName // Fallback to full namespace name
	}
	
	matches := re.FindStringSubmatch(namespaceName)
	if len(matches) >= 2 {
		// Use first capture group as workload ID
		extractedID := matches[1]
		w.logger.Info("workload-extraction", "✅ Extracted workload ID '"+extractedID+"' from namespace '"+namespaceName+"' using pattern '"+w.workloadIDPattern+"'")
		return extractedID
	}
	
	// Fallback: use namespace name as workload ID
	w.logger.Info("workload-extraction", "⚠️  No capture group match, using full namespace name as workload ID")
	return namespaceName
}

// ensureUnifiedControllerStarted creates and starts the unified controller if not already started
func (w *WorkloadMonitor) ensureUnifiedControllerStarted() error {
	if w.unifiedController != nil {
		return nil // Already started
	}
	
	w.logger.Info("workload-controller", "["+w.clusterName+"] 🚀 Starting unified controller for workload monitoring")
	
	// Create unified controller for workload resources
	unifiedConfig := &faro.Config{
		OutputDir:  w.logDir,
		LogLevel:   w.logLevel,  // Use same log level as main logger
		JsonExport: true,
        Resources:  []faro.ResourceConfig{}, // Start empty, resources added dynamically
    }
	unifiedController := faro.NewController(w.client, w.logger, unifiedConfig)
	w.unifiedController = unifiedController
	
	// Add JSON middleware for workload annotation injection (happens BEFORE JSON logging)
	workloadMiddleware := &WorkloadJSONMiddleware{Monitor: w}
	unifiedController.AddJSONMiddleware(workloadMiddleware)
	
	// Add deleted resource middleware for UUID tracking (happens BEFORE JSON logging)
	deletedMiddleware := &DeletedResourceMiddleware{Monitor: w}
	unifiedController.AddJSONMiddleware(deletedMiddleware)
	
	// Add event handler for dynamic GVR discovery (happens AFTER JSON logging)
	unifiedHandler := &UnifiedWorkloadHandler{Monitor: w}
	unifiedController.AddEventHandler(unifiedHandler)
	
	// Start the unified controller in a goroutine
	go func() {
		if err := unifiedController.Start(); err != nil {
			w.logger.Error("workload-controller", "["+w.clusterName+"] Unified controller failed: "+err.Error())
		}
	}()
	
	return nil
}

// addWorkloadToUnifiedController adds workload resources to the unified controller
func (w *WorkloadMonitor) addWorkloadToUnifiedController(workloadID, workloadName string, namespaces []string) {
	if len(namespaces) == 0 {
		w.logger.Info("workload-controller", "["+w.clusterName+"] ⚠️  No namespaces found for workload "+workloadID+", skipping resource addition")
		return
	}
	
	if len(w.cmdNamespaceGVRs) == 0 {
		w.logger.Info("workload-controller", "["+w.clusterName+"] ⚠️  No namespace GVRs configured, skipping resource addition for workload "+workloadID)
		return
	}
	
	// Ensure unified controller is started
	if err := w.ensureUnifiedControllerStarted(); err != nil {
		w.logger.Error("workload-controller", "["+w.clusterName+"] Failed to start unified controller: "+err.Error())
		return
	}
	
	w.logger.Info("workload-controller", "["+w.clusterName+"] 🚀 Adding workload "+workloadID+" resources to unified controller ("+strconv.Itoa(len(namespaces))+" namespaces)")
	
	// Create resource configs for this workload's namespaces - one config per namespace per GVR
	var newResourceConfigs []faro.ResourceConfig
	for _, gvr := range w.cmdNamespaceGVRs {
		scope := w.determineGVRScope(gvr, []string{}) // Simplified - assume namespaced by default
		for _, namespace := range namespaces {
		newResourceConfigs = append(newResourceConfigs, faro.ResourceConfig{
		GVR:               gvr,
		Scope:             scope,
			NamespaceNames: []string{namespace}, // Single namespace per config
		})
		}
	}
	
	// Add resources to the unified controller
	w.unifiedController.AddResources(newResourceConfigs)
	
	// Start informers for the new resources
	if err := w.unifiedController.StartNewInformers(); err != nil {
		w.logger.Error("workload-controller", "["+w.clusterName+"] Failed to start informers for workload "+workloadID+": "+err.Error())
		return
	}
	
	w.logger.Info("workload-controller", 
		"["+w.clusterName+"] ✅ Added workload "+workloadID+" to unified controller: "+strconv.Itoa(len(w.cmdNamespaceGVRs))+" GVRs in "+strconv.Itoa(len(namespaces))+" namespaces")
}

// identifyWorkloadFromNamespace determines which workload a namespace belongs to
func (w *WorkloadMonitor) identifyWorkloadFromNamespace(namespace string) (string, string) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	
	// Check all detected workloads to see if this namespace belongs to any of them
	for workloadID, namespaces := range w.detectedWorkloads {
		for _, ns := range namespaces {
			if ns == namespace {
				workloadName := w.workloadIDToWorkloadName[workloadID]
				// Removed repetitive debug log - this gets called for every event
				return workloadID, workloadName
			}
		}
	}
	// Removed repetitive debug log - this gets called for every unrelated event
	return "", ""
}

// getWorkloadNamespaces returns all namespaces for a given workload ID
func (w *WorkloadMonitor) getWorkloadNamespaces(workloadID string) []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.detectedWorkloads[workloadID]
}

// discoverWorkloadNamespaces finds all namespaces related to a workload by name pattern matching
func (w *WorkloadMonitor) discoverWorkloadNamespaces(workloadID, workloadName string) []string {
	w.logger.Debug("workload-discovery", "["+w.clusterName+"] 🔍 Discovering all namespaces for workload '"+workloadID+"' ("+workloadName+")")
	
	// List all namespaces
	unstructuredList, err := w.client.Dynamic.Resource(schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "namespaces",
	}).List(context.TODO(), metav1.ListOptions{})
	
	if err != nil {
		w.logger.Error("workload-discovery", "Failed to list namespaces: "+err.Error())
		return []string{}
	}
	
	namespaceSet := make(map[string]bool)
	
	w.logger.Debug("workload-discovery", "["+w.clusterName+"] 📊 Total namespaces to examine: "+strconv.Itoa(len(unstructuredList.Items)))
	
	// Apply workload ID pattern matching
	for _, item := range unstructuredList.Items {
		nsName := item.GetName()
		labels := item.GetLabels()
		
		w.logger.Debug("workload-discovery", "["+w.clusterName+"] 🔍 Examining namespace: "+nsName)
		
		// Check 1: namespace name contains the extracted workload ID
		// The workloadID is now the extracted shared identifier (e.g., "2l4e01bhmbec53h62riq28ej9clnpfk1")
		if strings.Contains(nsName, workloadID) {
			w.logger.Debug("workload-discovery", "["+w.clusterName+"] ✅ Match by name: "+nsName+" contains '"+workloadID+"'")
			namespaceSet[nsName] = true
		} else {
			w.logger.Debug("workload-discovery", "["+w.clusterName+"] ❌ No name match: "+nsName+" does not contain '"+workloadID+"'")
		}
		
		// Check 2: namespace has the detection label matching the workloadName
		if labelWorkloadName, exists := labels[w.detectionLabel]; exists && labelWorkloadName == workloadName {
			w.logger.Debug("workload-discovery", "["+w.clusterName+"] ✅ Match by label: "+nsName+" has "+w.detectionLabel+"="+workloadName)
			namespaceSet[nsName] = true
		} else if exists {
			w.logger.Debug("workload-discovery", "["+w.clusterName+"] ❌ Label mismatch: "+nsName+" has "+w.detectionLabel+"="+labelWorkloadName+" (expected: "+workloadName+")")
		} else {
			w.logger.Debug("workload-discovery", "["+w.clusterName+"] ❌ No label: "+nsName+" missing "+w.detectionLabel)
		}
	}
	
	// Convert set to slice
	var namespaces []string
	for ns := range namespaceSet {
		namespaces = append(namespaces, ns)
	}
	
	w.logger.Info("workload-discovery", "["+w.clusterName+"] 📋 Found "+strconv.Itoa(len(namespaces))+" namespaces for workload '"+workloadID+"': "+fmt.Sprintf("%v", namespaces))
	
	return namespaces
}

// handlePotentialWorkloadNamespace checks if a namespace without labels belongs to an existing workload
// DEAD CODE: handlePotentialWorkloadNamespace - Not used in current test scenarios
// TODO: Remove after test verification - only needed for late namespace creation scenarios
/*
func (w *WorkloadMonitor) handlePotentialWorkloadNamespace(namespaceName string) {
	w.mu.RLock()
	existingWorkloads := make(map[string]string)
	for workloadID, workloadName := range w.workloadIDToWorkloadName {
		existingWorkloads[workloadID] = workloadName
	}
	w.mu.RUnlock()
	
	// Check if this namespace belongs to any existing workload
	for workloadID, workloadName := range existingWorkloads {
		if strings.Contains(namespaceName, workloadID) {
			w.logger.Info("workload-detection", "["+w.clusterName+"] 🔗 Found late-created namespace: "+namespaceName+" for workload "+workloadID)
			// Re-trigger workload detection to update the controller
			w.reevaluateWorkloadNamespaces(workloadID, workloadName)
			break
		}
	}
}
*/

// DEAD CODE: reevaluateWorkloadNamespaces - Not used in current test scenarios  
// TODO: Remove after test verification - only needed for namespace updates
/*
// reevaluateWorkloadNamespaces re-discovers namespaces for an existing workload
func (w *WorkloadMonitor) reevaluateWorkloadNamespaces(workloadID, workloadName string) {
	w.logger.Info("workload-monitoring", "["+w.clusterName+"] 🔄 Re-evaluating namespaces for workload "+workloadID)
	
	// Discover all namespaces related to this workload
	namespaces := w.discoverWorkloadNamespaces(workloadID, workloadName)
	
	w.mu.Lock()
	previousNamespaces := w.detectedWorkloads[workloadID]
	w.detectedWorkloads[workloadID] = namespaces
	w.mu.Unlock()
	
	if len(namespaces) > len(previousNamespaces) {
		w.logger.Info("workload-monitoring", 
			"["+w.clusterName+"] 📋 Found "+strconv.Itoa(len(namespaces)-len(previousNamespaces))+" NEW namespaces for workload "+workloadID)
		
		// Add new namespaces to the unified controller
		w.addWorkloadToUnifiedController(workloadID, workloadName, namespaces)
	} else {
		w.logger.Info("workload-monitoring", "["+w.clusterName+"] No new namespaces found for workload "+workloadID)
	}
}
*/

// DEAD CODE: discoverAllNamespacedGVRs - Never called in practice, replaced with simplified scope detection
// TODO: Remove after test verification - API discovery overhead not needed
/*
// DEAD CODE: discoverAllNamespacedGVRs discovers all available namespaced GVRs in the cluster
// This function is not used in the current unified controller architecture
/*
func (w *WorkloadMonitor) discoverAllNamespacedGVRs() []string {
	// Get all API resources
	_, apiResourceLists, err := w.client.Discovery.ServerGroupsAndResources()
	if err != nil {
		w.logger.Error("workload-monitoring", "Failed to discover API resources: "+err.Error())
		return []string{}
	}
	
	var gvrList []string
	
	for _, apiResourceList := range apiResourceLists {
		gv, err := schema.ParseGroupVersion(apiResourceList.GroupVersion)
		if err != nil {
			continue
		}
		
		for _, apiResource := range apiResourceList.APIResources {
			// Skip subresources (contain '/')
			if strings.Contains(apiResource.Name, "/") {
				continue
			}
			
			// Only include namespaced resources
			if apiResource.Namespaced {
				var gvr string
				if gv.Group == "" {
					gvr = gv.Version + "/" + apiResource.Name
				} else {
					gvr = gv.Group + "/" + gv.Version + "/" + apiResource.Name
				}
				gvrList = append(gvrList, gvr)
			}
		}
	}
	
	return gvrList
}
*/

// DEAD CODE: discoverAllClusterScopedGVRs discovers all available cluster-scoped GVRs in the cluster
// This function is not used in the current unified controller architecture
/*
func (w *WorkloadMonitor) discoverAllClusterScopedGVRs() []string {
	// Get all API resources
	_, apiResourceLists, err := w.client.Discovery.ServerGroupsAndResources()
	if err != nil {
		w.logger.Error("workload-monitoring", "Failed to discover API resources: "+err.Error())
		return []string{}
	}
	
	var gvrList []string
	
	for _, apiResourceList := range apiResourceLists {
		gv, err := schema.ParseGroupVersion(apiResourceList.GroupVersion)
		if err != nil {
			continue
		}
		
		for _, apiResource := range apiResourceList.APIResources {
			// Skip subresources (contain '/')
			if strings.Contains(apiResource.Name, "/") {
				continue
			}
			
			// Only include cluster-scoped resources
			if !apiResource.Namespaced {
				var gvr string
				if gv.Group == "" {
					gvr = gv.Version + "/" + apiResource.Name
				} else {
					gvr = gv.Group + "/" + gv.Version + "/" + apiResource.Name
				}
				gvrList = append(gvrList, gvr)
			}
		}
	}
	
	return gvrList
}
*/

// determineGVRScope determines if a GVR is namespaced or cluster-scoped
func (w *WorkloadMonitor) determineGVRScope(gvr string, namespacedGVRs []string) faro.Scope {
	// Check if GVR is in the namespaced list
	for _, namespacedGVR := range namespacedGVRs {
		if gvr == namespacedGVR {
			return faro.NamespaceScope
		}
	}
	return faro.ClusterScope
}

// filterGVRs applies cluster/namespace GVR filtering
// DEAD CODE: filterGVRs - Not used in current unified controller architecture
// TODO: Remove after test verification - replaced by explicit resource configuration
/*
// FILTERING STRATEGY (Faro-aligned):
// 1. Use clustergvrs for cluster-scoped resources (monitored cluster-wide)
// 2. Use namespacegvrs for namespace-scoped resources (monitored per-namespace for detected workloads)
// 3. No denylist - explicit inclusion only
func (w *WorkloadMonitor) filterGVRs(allGVRs []string) []string {
	// Cluster GVRs - Resources to monitor cluster-wide
	// Priority: 1. Command-line flag, 2. Built-in defaults
	var clusterGVRs []string
	if len(w.cmdClusterGVRs) > 0 {
		// Use command-line cluster GVRs
		clusterGVRs = w.cmdClusterGVRs
	} else {
		// Built-in cluster GVRs defaults (for workload detection)
		clusterGVRs = []string{
			"v1/namespaces", // Essential for workload detection
		}
	}
	
	// Create lookup set for cluster GVRs
	clusterSet := make(map[string]bool)
	for _, gvr := range clusterGVRs {
		clusterSet[gvr] = true
	}
	
	// Namespace GVRs are handled separately in per-namespace monitoring
	// They are excluded from cluster-wide monitoring to prevent duplication
	namespaceSet := make(map[string]bool)
	if len(w.cmdNamespaceGVRs) > 0 {
		w.logger.Info("filtering", "📦 Excluding "+strconv.Itoa(len(w.cmdNamespaceGVRs))+" namespace GVRs from cluster-wide monitoring (will monitor per-namespace)")
		for _, gvr := range w.cmdNamespaceGVRs {
			namespaceSet[gvr] = true
		}
	}
	
	// Apply filtering: only include cluster GVRs, exclude namespace GVRs
	var filteredGVRs []string
	for _, gvr := range allGVRs {
		if clusterSet[gvr] {
			// Include cluster-scoped GVRs
				filteredGVRs = append(filteredGVRs, gvr)
		} else if !namespaceSet[gvr] && len(clusterGVRs) == 0 {
			// If no cluster GVRs specified, include all except namespace GVRs
			filteredGVRs = append(filteredGVRs, gvr)
		}
	}
	
	// Log filtering strategy and results
	clusterGVRCount := len(clusterGVRs)
	namespaceGVRCount := len(w.cmdNamespaceGVRs)
	
	if clusterGVRCount > 0 {
		w.logger.Info("filtering", "🌐 CLUSTER MODE: Monitoring "+strconv.Itoa(len(filteredGVRs))+" cluster-scoped GVRs")
	} else {
		excludedCount := len(allGVRs) - len(filteredGVRs)
		w.logger.Info("filtering", "🔍 AUTO MODE: Monitoring "+strconv.Itoa(len(filteredGVRs))+" GVRs (excluded "+strconv.Itoa(excludedCount)+" namespace-scoped)")
	}
	
	if namespaceGVRCount > 0 {
		w.logger.Info("filtering", "📋 NAMESPACE MODE: "+strconv.Itoa(namespaceGVRCount)+" GVRs will be monitored per-namespace for detected workloads")
	}
	
	return filteredGVRs
}
*/


// detectClusterName attempts to detect the cluster name using kubeps1-style approach
func detectClusterName(client *faro.KubernetesClient) string {
	// Priority 1: Use kubectl current-context (kubeps1 approach)
	// This is what kubectx/kubens and kubeps1 use - most reliable and standard
	homeDir, err := os.UserHomeDir()
	if err == nil {
		kubeconfigPath := filepath.Join(homeDir, ".kube", "config")
		if data, err := os.ReadFile(kubeconfigPath); err == nil {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "current-context:") {
					parts := strings.Split(line, ":")
					if len(parts) > 1 {
						context := strings.TrimSpace(parts[1])
						if context != "" {
							return context
						}
					}
				}
			}
		}
	}
	
	// Priority 2: Try OpenShift cluster version (for OpenShift environments)
	clusterVersionResource := schema.GroupVersionResource{
		Group:    "config.openshift.io",
		Version:  "v1",
		Resource: "clusterversions",
	}
	
	cvList, err := client.Dynamic.Resource(clusterVersionResource).List(context.TODO(), metav1.ListOptions{})
	if err == nil && len(cvList.Items) > 0 {
		cv := cvList.Items[0]
		if name := cv.GetName(); name != "" {
			return "openshift-" + name
		}
	}
	
	// Priority 3: Fallback to kube-system UID (last resort)
	nsResource := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "namespaces",
	}
	
	kubeSystemNS, err := client.Dynamic.Resource(nsResource).Get(context.TODO(), "kube-system", metav1.GetOptions{})
	if err == nil {
		if uid := kubeSystemNS.GetUID(); uid != "" {
			// Use first 8 characters of kube-system UID as cluster identifier
			uidStr := string(uid)
			if len(uidStr) >= 8 {
				return "cluster-" + uidStr[:8]
			}
		}
	}
	
	// Final fallback
	return "unknown-cluster"
}

// handleDynamicGVRDiscovery processes v1/events to discover new GVRs from involvedObject
func (w *WorkloadMonitor) handleDynamicGVRDiscovery(event faro.MatchedEvent, workloadID string, namespaces []string) {
	// Only process ADDED/UPDATED events (DELETE events don't have involvedObject)
	if event.EventType != "ADDED" && event.EventType != "UPDATED" {
		return
	}
	
	// Extract involvedObject from the event
	involvedObj, found, err := unstructured.NestedMap(event.Object.Object, "involvedObject")
	if err != nil || !found || involvedObj == nil {
		return
	}
	
	// Extract GVR from involvedObject
	discoveredGVR := w.extractGVRFromInvolvedObject(involvedObj)
	if discoveredGVR == "" {
		return
	}
	
	// Check if this GVR is already being monitored in any of the workload's namespaces
	namespace := event.Object.GetNamespace()
	if w.isGVRAlreadyMonitored(discoveredGVR, namespace) {
		return
	}
	
	// Add this GVR to dynamic tracking
	w.mu.Lock()
	if !w.dynamicGVRs[discoveredGVR] {
		w.dynamicGVRs[discoveredGVR] = true
		// Simplified: Only log when actually adding (combines discovery + addition)
		w.logger.Info("dynamic-discovery", 
			"["+w.clusterName+"] 🔍 Discovered and adding GVR '"+discoveredGVR+"' from v1/events in workload "+workloadID)
	}
	
	// Track per-namespace monitoring
	if w.discoveredGVRs[namespace] == nil {
		w.discoveredGVRs[namespace] = make(map[string]bool)
	}
	
	// Check if this workload already has this GVR configured
	alreadyConfigured := w.discoveredGVRs[namespace][discoveredGVR]
	w.discoveredGVRs[namespace][discoveredGVR] = true
	w.mu.Unlock()
	
	// Only add the GVR if this workload doesn't already have it configured
	if !alreadyConfigured {
		w.addGVRToWorkloadController(workloadID, discoveredGVR, namespaces)
	}
}

// extractGVRFromInvolvedObject converts involvedObject apiVersion+kind to GVR format
func (w *WorkloadMonitor) extractGVRFromInvolvedObject(involvedObj map[string]interface{}) string {
	apiVersion, ok := involvedObj["apiVersion"].(string)
	if !ok || apiVersion == "" {
		w.logger.Debug("gvr-extraction", "["+w.clusterName+"] ❌ Failed to extract apiVersion from involvedObject")
		return ""
	}
	
	kind, ok := involvedObj["kind"].(string)
	if !ok || kind == "" {
		w.logger.Debug("gvr-extraction", "["+w.clusterName+"] ❌ Failed to extract kind from involvedObject")
		return ""
	}
	
	// Convert apiVersion + kind to GVR format
	// Examples:
	// - apiVersion: "v1", kind: "Pod" -> "v1/pods"
	// - apiVersion: "batch/v1", kind: "Job" -> "batch/v1/jobs"
	// - apiVersion: "apps/v1", kind: "Deployment" -> "apps/v1/deployments"
	
	resource := strings.ToLower(kind) + "s" // Simple pluralization (works for most cases)
	
	// Handle special cases
	switch kind {
	case "Endpoints":
		resource = "endpoints" // Already plural
	case "NetworkPolicy":
		resource = "networkpolicies"
	case "Ingress":
		resource = "ingresses"
	}
	
	gvr := apiVersion + "/" + resource
	w.logger.Debug("gvr-extraction", "["+w.clusterName+"] 🔍 Extracted GVR '"+gvr+"' from kind '"+kind+"' (apiVersion: "+apiVersion+")")
	return gvr
}

// isGVRAlreadyMonitored checks if a GVR is already being monitored in the given namespace
func (w *WorkloadMonitor) isGVRAlreadyMonitored(gvr, namespace string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	
	// Check if it's in initial configuration
	if w.initialGVRs[gvr] {
		return true
	}
	
	// Check if it's already discovered for this namespace
	if nsGVRs, exists := w.discoveredGVRs[namespace]; exists {
		return nsGVRs[gvr]
	}
	
	return false
}

// addGVRToWorkloadController gracefully adds a new GVR to an existing workload controller
func (w *WorkloadMonitor) addGVRToWorkloadController(workloadID, newGVR string, namespaces []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	// Ensure unified controller is started
	if err := w.ensureUnifiedControllerStarted(); err != nil {
		w.logger.Error("dynamic-discovery", "["+w.clusterName+"] Failed to start unified controller: "+err.Error())
		return
	}
	
	// Removed redundant "Adding GVR" message - only log final result
	
	// Create resource configs for the new GVR - one config per namespace
	var newResourceConfigs []faro.ResourceConfig
	scope := w.determineGVRScope(newGVR, []string{}) // Simplified - assume namespaced by default
	
	for _, namespace := range namespaces {
		newResourceConfigs = append(newResourceConfigs, faro.ResourceConfig{
			GVR:               newGVR,
			Scope:             scope,
			NamespaceNames: []string{namespace},
		})
	}
	
	// Add the new resource configurations to the unified controller
	w.unifiedController.AddResources(newResourceConfigs)
	
	// Start informers for the new GVR configuration
	// This will start informers only for new GVRs that don't already have active informers
	if err := w.unifiedController.StartNewInformers(); err != nil {
		w.logger.Warning("dynamic-discovery", 
			"["+w.clusterName+"] Failed to start informers for new GVR "+newGVR+": "+err.Error())
		return
	}
	
	w.logger.Info("dynamic-discovery", 
		"["+w.clusterName+"] ✅ Successfully added GVR '"+newGVR+"' to unified controller for workload "+workloadID)
}

// logDynamicGVRSummary logs all dynamically discovered GVRs on shutdown
func (w *WorkloadMonitor) logDynamicGVRSummary() {
	w.mu.RLock()
	defer w.mu.RUnlock()
	
	if len(w.dynamicGVRs) == 0 {
		w.logger.Info("shutdown", "["+w.clusterName+"] 📊 No GVRs were dynamically discovered during runtime")
		return
	}
	
	var discoveredList []string
	for gvr := range w.dynamicGVRs {
		discoveredList = append(discoveredList, gvr)
	}
	
	w.logger.Info("shutdown", 
		"["+w.clusterName+"] 📊 Dynamically discovered GVRs during runtime: "+strings.Join(discoveredList, ", "))
	
	// Log per-namespace breakdown
	for namespace, gvrs := range w.discoveredGVRs {
		var nsGVRs []string
		for gvr := range gvrs {
			if w.dynamicGVRs[gvr] { // Only log dynamically discovered ones
				nsGVRs = append(nsGVRs, gvr)
			}
		}
		if len(nsGVRs) > 0 {
			w.logger.Info("shutdown", 
				"["+w.clusterName+"] 📋 Namespace "+namespace+": "+strings.Join(nsGVRs, ", "))
		}
	}
}

func main() {
	// Parse command line flags
	discoverNamespaces := flag.String("discover-namespaces", "app.kubernetes.io/name~.*", "Find namespaces by label key and pattern (format: 'label-key~pattern')")
	extractFromNamespace := flag.String("extract-from-namespace", "env-staging-(.+)", "Regex pattern to extract workload identifier from main namespace names (use capture group)")
	clusterResources := flag.String("cluster-resources", "", "Comma-separated list of cluster-scoped GVRs to monitor (e.g., v1/namespaces)")
	namespaceResources := flag.String("namespace-resources", "", "Comma-separated list of namespace-scoped GVRs to create per-namespace informers for detected workloads")
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warning, error, fatal)")
	flag.Parse()

	// Capture full command line for logging
	commandLine := strings.Join(os.Args, " ")

	// Parse discover-namespaces flag (format: "label-key~pattern")
	var detectionLabel, workloadPattern string
	if strings.Contains(*discoverNamespaces, "~") {
		parts := strings.SplitN(*discoverNamespaces, "~", 2)
		detectionLabel = parts[0]
		workloadPattern = parts[1]
	} else {
		log.Fatalf("Invalid discover-namespaces format '%s'. Expected format: 'label-key~pattern'", *discoverNamespaces)
	}

	// Compile the regex pattern
	namePattern, err := regexp.Compile(workloadPattern)
	if err != nil {
		log.Fatalf("Invalid regex pattern '%s': %v", workloadPattern, err)
	}

	// Create Faro client
	client, err := faro.NewKubernetesClient()
	if err != nil {
		log.Fatalf("Failed to create Faro client: %v", err)
	}

	// Auto-detect cluster name from kubectl context
	detectedClusterName := detectClusterName(client)

	// Validate log level
	validLevels := map[string]bool{
		"debug": true, "info": true, "warning": true, "error": true, "fatal": true,
	}
	if !validLevels[*logLevel] {
		log.Fatalf("Invalid log level '%s'. Valid levels: debug, info, warning, error, fatal", *logLevel)
	}

	// Create logger
	logDir := "./logs/workload-monitor"
	// Create config for logger
	loggerConfig := &faro.Config{OutputDir: logDir, LogLevel: *logLevel, JsonExport: true}
	logger, err := faro.NewLogger(loggerConfig)
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	// Log startup messages with full context
	logger.Info("startup", "🚀 Workload Monitor Starting (Scalable Version)")
	logger.Info("startup", "🏛️  Cluster: "+detectedClusterName)
	logger.Info("startup", "💻 Command: "+commandLine)
	logger.Info("startup", "🔍 Discover namespaces: "+*discoverNamespaces)
	logger.Info("startup", "🏷️  Detection label: "+detectionLabel)
	logger.Info("startup", "📋 Workload pattern: "+workloadPattern)
	logger.Info("startup", "📁 Extract from namespace: "+*extractFromNamespace)
	if *clusterResources != "" {
		logger.Info("startup", "🌐 Cluster resources: "+*clusterResources)
	}
	if *namespaceResources != "" {
		logger.Info("startup", "📋 Namespace resources (per-namespace): "+*namespaceResources)
	}

	// Parse GVR lists from command line
	var cmdClusterGVRs, cmdNamespaceGVRs []string
	if *clusterResources != "" {
		cmdClusterGVRs = strings.Split(*clusterResources, ",")
		for i, gvr := range cmdClusterGVRs {
			cmdClusterGVRs[i] = strings.TrimSpace(gvr)
		}
	}
	if *namespaceResources != "" {
		cmdNamespaceGVRs = strings.Split(*namespaceResources, ",")
		for i, gvr := range cmdNamespaceGVRs {
			cmdNamespaceGVRs[i] = strings.TrimSpace(gvr)
		}
	}

	// Create monitor
	monitor := &WorkloadMonitor{
		client:                   client,
		logger:                   logger,
		detectionLabel:           detectionLabel,
		workloadNamePattern:      namePattern,
		workloadIDPattern:        *extractFromNamespace,
		logDir:                   logDir,
		logLevel:                 *logLevel,
		clusterName:              detectedClusterName,
		commandLine:              commandLine,
		detectedWorkloads:        make(map[string][]string),
		workloadIDToWorkloadName: make(map[string]string),
		cmdClusterGVRs:           cmdClusterGVRs,
		cmdNamespaceGVRs:         cmdNamespaceGVRs,
		discoveredGVRs:           make(map[string]map[string]bool),
		initialGVRs:              make(map[string]bool),
		dynamicGVRs:              make(map[string]bool),
	}
	
	// Track initially configured GVRs
	for _, gvr := range cmdNamespaceGVRs {
		monitor.initialGVRs[gvr] = true
	}
	
	// Start discovery controller for namespace monitoring
	logger.Info("startup", "🔧 Setting up discovery controller for workload detection")
	logger.Info("startup", "🏷️  Client-side namespace filtering by label key: "+detectionLabel)
	
	// Create discovery config - only monitor namespaces for workload detection
	var discoveryResourceConfigs []faro.ResourceConfig
	
	// Add cluster-scoped GVRs if specified
	if len(cmdClusterGVRs) > 0 {
		for _, gvr := range cmdClusterGVRs {
			// Assume cluster GVRs are cluster-scoped (they should be validated separately)
			discoveryResourceConfigs = append(discoveryResourceConfigs, faro.ResourceConfig{
			GVR:   gvr,
				Scope: faro.ClusterScope,
			})
		}
	}
	
	// Always add v1/namespaces for workload detection - watch ALL namespaces, filter in code
	discoveryResourceConfigs = append(discoveryResourceConfigs, faro.ResourceConfig{
		GVR:   "v1/namespaces",
		Scope: faro.ClusterScope,
		// No LabelSelector - we want to see ALL namespaces and filter in handleNamespaceDetection
	})
	
	discoveryConfig := &faro.Config{
		OutputDir: logDir,
		LogLevel:  *logLevel,  // Use command line log level
		JsonExport: true,
		Resources: discoveryResourceConfigs,
	}

	// Create discovery controller
	discoveryController := faro.NewController(client, logger, discoveryConfig)
	monitor.discoveryController = discoveryController

	// Register the monitor as an event handler for discovery
	discoveryController.AddEventHandler(monitor)

	// Unified controller will be created lazily when first workload is detected
	monitor.unifiedController = nil

	// Set up graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		logger.Info("shutdown", 
			"Received signal " + sig.String() + ", shutting down gracefully...")
		cancel()
	}()

	// Consolidated startup message
	resourceInfo := ""
	if len(cmdNamespaceGVRs) > 0 {
		resourceInfo = " | Workload resources: " + strings.Join(cmdNamespaceGVRs, ", ")
	}
	logger.Info("startup", "✅ Ready for workload detection with " + strconv.Itoa(len(discoveryResourceConfigs)) + " discovery resources" + resourceInfo)

	// Start the discovery controller in a goroutine
	go func() {
		if err := discoveryController.Start(); err != nil {
			logger.Error("startup", "Discovery controller failed: "+err.Error())
			os.Exit(1)
		}
	}()

	// Unified controller will be started lazily when first workload is detected

	// Wait for shutdown signal
	<-ctx.Done()
	
	// Log dynamic GVR discovery summary before shutdown
	monitor.logDynamicGVRSummary()
	
	logger.Info("shutdown", "✅ Scalable workload monitor stopped gracefully")
}