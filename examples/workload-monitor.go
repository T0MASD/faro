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
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// WorkloadMonitor handles dynamic discovery and monitoring of workloads
type WorkloadMonitor struct {
	client                   *faro.KubernetesClient
	logger                   *faro.Logger
	discoveryController      *faro.Controller         // Cluster controller for namespace discovery
	workloadControllers      map[string]*faro.Controller // Per-workload controllers for namespace GVRs
	mu                       sync.RWMutex
	
	// Configuration
	detectionLabel           string        // Label key to look for (e.g., "app.kubernetes.io/name")
	workloadNamePattern      *regexp.Regexp // Pattern to match workload names
	workloadIDPattern        string        // Pattern to extract workload ID from namespace names (e.g., "ocm-staging-(.+)")
	logDir                   string
	cmdClusterGVRs           []string      // Command-line cluster-scoped GVRs
	cmdNamespaceGVRs         []string      // Command-line namespace-scoped GVRs (per-namespace informers)
	
	// Context information
	clusterName              string        // Name/identifier of the cluster being monitored
	commandLine              string        // Full command line used to start the monitor
	
	// State tracking
	detectedWorkloads        map[string][]string      // workloadID -> namespaces
	workloadIDToWorkloadName map[string]string
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

// WorkloadResourceHandler handles events for a specific workload's resources
type WorkloadResourceHandler struct {
	WorkloadID   string
	WorkloadName string
	Namespaces   []string
	Monitor      *WorkloadMonitor
}

func (w *WorkloadResourceHandler) OnMatched(event faro.MatchedEvent) error {
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
			ResourceName: event.Object.GetName(),
			Action:       event.EventType,
			UID:          string(uid),
			Labels:       labels,
		},
	}
	
	// Marshal to JSON and log
	jsonData, err := json.Marshal(entry)
	if err != nil {
		w.logger.Error("workload-handler", "Failed to marshal log entry: "+err.Error())
		return err
	}
	
	w.logger.Info("workload-handler", string(jsonData))
	return nil
}

// handleNamespaceDetection processes namespace events for workload discovery
func (w *WorkloadMonitor) handleNamespaceDetection(event faro.MatchedEvent) error {
	namespaceName := event.Object.GetName()
	labels := event.Object.GetLabels()
	
	// Check if namespace has the detection label
	workloadName, exists := labels[w.detectionLabel]
	if !exists {
		// Also check if this namespace belongs to an existing workload (for late-created namespaces)
		w.handlePotentialWorkloadNamespace(namespaceName)
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
	
	// Discover all namespaces for this workload ID
	workloadNamespaces := w.discoverWorkloadNamespaces(workloadID, workloadName)
	
	w.mu.Lock()
	isNewWorkload := w.detectedWorkloads[workloadID] == nil
	previousNamespaces := w.detectedWorkloads[workloadID]
	w.detectedWorkloads[workloadID] = workloadNamespaces
	hasController := w.workloadControllers[workloadID] != nil
	if isNewWorkload {
		w.workloadIDToWorkloadName[workloadID] = workloadName
	}
	w.mu.Unlock()
	
	if isNewWorkload {
		w.logger.Info("workload-detection", "["+w.clusterName+"] 🚀 New workload detected: "+workloadID+" with namespaces: "+fmt.Sprintf("%v", workloadNamespaces))
		w.createWorkloadController(workloadID, workloadName, workloadNamespaces)
	} else if !hasController && len(workloadNamespaces) > len(previousNamespaces) {
		w.logger.Info("workload-detection", "["+w.clusterName+"] 🔄 Workload "+workloadID+" updated with more namespaces: "+fmt.Sprintf("%v", workloadNamespaces))
		w.createWorkloadController(workloadID, workloadName, workloadNamespaces)
	} else {
		w.logger.Info("workload-detection", "["+w.clusterName+"] 🔄 Workload "+workloadID+" already has controller or no new namespaces")
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

// createWorkloadController creates a dedicated controller for a workload's namespaces
func (w *WorkloadMonitor) createWorkloadController(workloadID, workloadName string, namespaces []string) {
	if len(namespaces) == 0 {
		w.logger.Info("workload-controller", "["+w.clusterName+"] ⚠️  No namespaces found for workload "+workloadID+", skipping controller creation")
		return
	}
	
	if len(w.cmdNamespaceGVRs) == 0 {
		w.logger.Info("workload-controller", "["+w.clusterName+"] ⚠️  No namespace GVRs configured, skipping controller creation for workload "+workloadID)
		return
	}
	
	w.logger.Info("workload-controller", "["+w.clusterName+"] 🚀 Creating dedicated controller for workload "+workloadID+" with "+strconv.Itoa(len(namespaces))+" namespaces")
	
	// Create config for this workload's namespaces
	var resourceConfigs []faro.ResourceConfig
	for _, gvr := range w.cmdNamespaceGVRs {
		scope := w.determineGVRScope(gvr, w.discoverAllNamespacedGVRs())
		resourceConfigs = append(resourceConfigs, faro.ResourceConfig{
			GVR:               gvr,
			Scope:             scope,
			NamespacePatterns: namespaces, // Server-side filtering for this workload only
		})
	}
	
	workloadConfig := &faro.Config{
		OutputDir:  fmt.Sprintf("%s/workload-%s", w.logDir, workloadID),
		LogLevel:   "info",
		JsonExport: true,
		Resources:  resourceConfigs,
	}
	
	// Create dedicated controller for this workload
	controller := faro.NewController(w.client, w.logger, workloadConfig)
	
	// Create workload-specific event handler
	handler := &WorkloadResourceHandler{
		WorkloadID:   workloadID,
		WorkloadName: workloadName,
		Namespaces:   namespaces,
		Monitor:      w,
	}
	controller.AddEventHandler(handler)
	
	// Set up readiness callback
	controller.SetReadyCallback(func() {
		w.logger.Info("workload-controller", "["+w.clusterName+"] ✅ Workload controller for "+workloadID+" is ready!")
	})
	
	// Store the controller
	w.mu.Lock()
	w.workloadControllers[workloadID] = controller
	w.mu.Unlock()
	
	// Start controller
	go func() {
		w.logger.Info("workload-controller", "["+w.clusterName+"] 🎯 Starting workload controller for "+workloadID)
		if err := controller.Start(); err != nil {
			w.logger.Error("workload-controller", "["+w.clusterName+"] Failed to start workload controller for "+workloadID+": "+err.Error())
		}
	}()
	
	w.logger.Info("workload-controller", 
		"["+w.clusterName+"] ✅ Created workload controller for "+workloadID+" monitoring "+strconv.Itoa(len(w.cmdNamespaceGVRs))+" GVRs in "+strconv.Itoa(len(namespaces))+" namespaces")
}

// discoverWorkloadNamespaces finds all namespaces related to a workload (no logging - called by multiple functions)
func (w *WorkloadMonitor) discoverWorkloadNamespaces(workloadID, workloadName string) []string {
	// List all namespaces
	unstructuredList, err := w.client.Dynamic.Resource(schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "namespaces",
	}).List(context.TODO(), metav1.ListOptions{})
	
	if err != nil {
		w.logger.Error("workload-monitoring", "Failed to list namespaces: "+err.Error())
		return []string{}
	}
	
	namespaceSet := make(map[string]bool)
	
		// Apply workload ID pattern matching
	for _, item := range unstructuredList.Items {
		nsName := item.GetName()
		labels := item.GetLabels()
		
		// Check 1: namespace name contains the extracted workload ID
		// The workloadID is now the extracted shared identifier (e.g., "2l4e01bhmbec53h62riq28ej9clnpfk1")
		if strings.Contains(nsName, workloadID) {
			namespaceSet[nsName] = true
		}
		
		// Check 2: namespace has the detection label matching the workloadName
		if labelWorkloadName, exists := labels[w.detectionLabel]; exists && labelWorkloadName == workloadName {
			namespaceSet[nsName] = true
		}
	}
	
	// Convert set to slice
	var namespaces []string
	for ns := range namespaceSet {
		namespaces = append(namespaces, ns)
	}
	
	return namespaces
}

// handlePotentialWorkloadNamespace checks if a namespace without labels belongs to an existing workload
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

// reevaluateWorkloadNamespaces re-discovers namespaces for an existing workload
func (w *WorkloadMonitor) reevaluateWorkloadNamespaces(workloadID, workloadName string) {
	w.logger.Info("workload-monitoring", "["+w.clusterName+"] 🔄 Re-evaluating namespaces for workload "+workloadID)
	
	// Discover all namespaces related to this workload
	namespaces := w.discoverWorkloadNamespaces(workloadID, workloadName)
	
	w.mu.Lock()
	previousNamespaces := w.detectedWorkloads[workloadID]
	hasController := w.workloadControllers[workloadID] != nil
	w.detectedWorkloads[workloadID] = namespaces
	w.mu.Unlock()
	
	if len(namespaces) > len(previousNamespaces) {
		w.logger.Info("workload-monitoring", 
			"["+w.clusterName+"] 📋 Found "+strconv.Itoa(len(namespaces)-len(previousNamespaces))+" NEW namespaces for workload "+workloadID)
		
		if !hasController {
			// Create controller if we don't have one yet
			w.createWorkloadController(workloadID, workloadName, namespaces)
		} else {
			// TODO: In a full implementation, we might recreate the controller with updated namespaces
			// For now, we log that new namespaces were found
			w.logger.Info("workload-monitoring", "["+w.clusterName+"] ⚠️  Workload "+workloadID+" has new namespaces but controller already exists")
		}
	} else {
		w.logger.Info("workload-monitoring", "["+w.clusterName+"] No new namespaces found for workload "+workloadID)
	}
}

// discoverAllNamespacedGVRs discovers all available namespaced GVRs in the cluster
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

// discoverAllClusterScopedGVRs discovers all available cluster-scoped GVRs in the cluster
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

func main() {
	// Parse command line flags
	discoverNamespaces := flag.String("discover-namespaces", "app.kubernetes.io/name~.*", "Find namespaces by label key and pattern (format: 'label-key~pattern')")
	extractFromNamespace := flag.String("extract-from-namespace", "{workload-id}.*", "Pattern to extract workload identifier from namespace names (use {workload-id} as placeholder)")
	clusterResources := flag.String("cluster-resources", "", "Comma-separated list of cluster-scoped GVRs to monitor (e.g., v1/namespaces)")
	namespaceResources := flag.String("namespace-resources", "", "Comma-separated list of namespace-scoped GVRs to create per-namespace informers for detected workloads")
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

	// Create logger
	logDir := "./logs/workload-monitor"
	// Create config for logger
	loggerConfig := &faro.Config{OutputDir: logDir, JsonExport: true}
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
		workloadControllers:      make(map[string]*faro.Controller),
		detectionLabel:           detectionLabel,
		workloadNamePattern:      namePattern,
		workloadIDPattern:        *extractFromNamespace,
		logDir:                   logDir,
		clusterName:              detectedClusterName,
		commandLine:              commandLine,
		detectedWorkloads:        make(map[string][]string),
		workloadIDToWorkloadName: make(map[string]string),
		cmdClusterGVRs:           cmdClusterGVRs,
		cmdNamespaceGVRs:         cmdNamespaceGVRs,
	}
	
	// Start discovery controller for namespace monitoring
	logger.Info("startup", "🔧 Setting up discovery controller for workload detection")
	
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
	
	// Always add v1/namespaces for workload detection
	discoveryResourceConfigs = append(discoveryResourceConfigs, faro.ResourceConfig{
		GVR:   "v1/namespaces",
		Scope: faro.ClusterScope,
	})
	
	discoveryConfig := &faro.Config{
		OutputDir: logDir,
		LogLevel:  "info",
		JsonExport: true,
		Resources: discoveryResourceConfigs,
	}

	// Create discovery controller
	discoveryController := faro.NewController(client, logger, discoveryConfig)
	monitor.discoveryController = discoveryController

	// Register the monitor as an event handler for discovery
	discoveryController.AddEventHandler(monitor)

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

	logger.Info("startup", "✅ Discovery controller configured with " + strconv.Itoa(len(discoveryResourceConfigs)) + " resource types")
	logger.Info("startup", "🔍 Ready for workload detection and per-workload controller creation")
	if len(cmdNamespaceGVRs) > 0 {
		logger.Info("startup", "📋 Will create per-workload controllers for: " + strings.Join(cmdNamespaceGVRs, ", "))
	}

	// Start the discovery controller in a goroutine
	go func() {
		if err := discoveryController.Start(); err != nil {
			logger.Error("startup", "Discovery controller failed: "+err.Error())
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Info("shutdown", "✅ Scalable workload monitor stopped gracefully")
}