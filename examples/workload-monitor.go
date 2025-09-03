package main

import (
	"context"
	"encoding/json"
	"flag"
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
	sharedController         *faro.Controller
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
	detectedWorkloads        map[string]bool
	monitoredNamespaces      map[string]bool
	namespaceToWorkloadID    map[string]string
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

// OnMatched handles both namespace detection and resource logging
func (w *WorkloadMonitor) OnMatched(event faro.MatchedEvent) error {
	// Handle namespace events for workload detection (only for ADDED events)
	if event.GVR == "v1/namespaces" && event.EventType == "ADDED" {
		return w.handleNamespaceDetection(event)
	}
	
	// Handle all resource events with client-side filtering (monitoring is always active)
		return w.handleResourceEventWithClientFiltering(event)
}

// logDeleteEvent creates and logs a structured DELETE event from CONFIG [DELETED] messages
func (w *WorkloadMonitor) logDeleteEvent(gvr, namespace, name string) error {
	// Check if this namespace is monitored
	w.mu.RLock()
	workloadID, isMonitored := w.namespaceToWorkloadID[namespace]
	workloadName := w.workloadIDToWorkloadName[workloadID]
	w.mu.RUnlock()
	
	if !isMonitored {
		return nil // Skip non-monitored namespaces
	}
	
	entry := StructuredLogEntry{
		Timestamp: time.Now().UTC(),
		Level:     "info",
		Message:   "Kubernetes resource deleted",
		Workload: WorkloadContext{
			WorkloadID:   workloadID,
			WorkloadName: workloadName,
			Namespace:    namespace,
			ResourceType: gvr,
			ResourceName: name,
			Action:       "DELETED",
			// UID and Labels not available for deleted resources
		},
	}
	
	// Marshal to JSON and log
	jsonData, err := json.Marshal(entry)
	if err != nil {
		w.logger.Error("workload-handler", "Failed to marshal DELETE log entry: "+err.Error())
		return err
	}
	
	w.logger.Info("workload-handler", string(jsonData))
	return nil
}

// handleResourceEventWithClientFiltering processes resource events with client-side namespace filtering
func (w *WorkloadMonitor) handleResourceEventWithClientFiltering(event faro.MatchedEvent) error {
	namespace := event.Object.GetNamespace()
	
	// For cluster-scoped resources, check if they're workload-related
	if namespace == "" {
		// Special handling for v1/namespaces - check if they belong to monitored workloads
		if event.GVR == "v1/namespaces" {
			namespaceName := event.Object.GetName()
			w.mu.RLock()
			workloadID, isMonitored := w.namespaceToWorkloadID[namespaceName]
			workloadName := w.workloadIDToWorkloadName[workloadID]
			w.mu.RUnlock()
			
			if isMonitored {
				return w.logResourceEvent(event, workloadID, workloadName)
			}
		}
		// For other cluster-scoped resources, always log them
		return w.logResourceEvent(event, "cluster-scoped", "cluster-scoped")
	}
	
	// For namespaced resources, check if namespace is monitored
	w.mu.RLock()
	workloadID, isMonitored := w.namespaceToWorkloadID[namespace]
	workloadName := w.workloadIDToWorkloadName[workloadID]
	w.mu.RUnlock()
	
	if isMonitored {
		return w.logResourceEvent(event, workloadID, workloadName)
	}
	
	// Debug: log ignored events for spam pattern identification
	w.logger.Debug("client-filtering", "Ignoring event for non-monitored namespace: "+namespace+" (GVR: "+event.GVR+")")
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
	
	// Skip deleted namespaces - Faro automatically stops informers
	if event.EventType == "DELETED" {
		return nil
	}
	
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
	
	// Check if we've already detected this workload
	w.mu.Lock()
	isNewWorkload := !w.detectedWorkloads[workloadID]
	if isNewWorkload {
		// Mark as detected and store mapping
		w.detectedWorkloads[workloadID] = true
		w.workloadIDToWorkloadName[workloadID] = workloadName
	}
	w.mu.Unlock()
	
	if isNewWorkload {
		// Add new workload to client-side filtering
		w.addWorkloadToClientFiltering(workloadID, workloadName)
	} else {
		// Re-evaluate namespaces for existing workload (in case new namespaces were created)
		w.reevaluateWorkloadNamespaces(workloadID, workloadName)
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

// addWorkloadToClientFiltering adds a detected workload to client-side filtering
func (w *WorkloadMonitor) addWorkloadToClientFiltering(workloadID, workloadName string) {
	w.logger.Info("workload-monitoring", "["+w.clusterName+"] 🚀 Adding workload "+workloadID+" ("+workloadName+") to client-side filtering")
	
	// Discover all namespaces related to this workload
	namespaces := w.discoverWorkloadNamespaces(workloadID, workloadName)
	
	w.mu.Lock()
	var newNamespaces []string
	
	// Track new namespaces and store workload ID mapping
	for _, ns := range namespaces {
		if !w.monitoredNamespaces[ns] {
			w.monitoredNamespaces[ns] = true
			w.namespaceToWorkloadID[ns] = workloadID
			newNamespaces = append(newNamespaces, ns)
		}
	}
	w.mu.Unlock()
	
	if len(newNamespaces) == 0 {
		w.logger.Info("workload-monitoring", "No new namespaces to monitor")
		return
	}
	
	w.logger.Info("workload-monitoring", 
		"["+w.clusterName+"] 📋 Found " + strconv.Itoa(len(newNamespaces)) + " namespaces for workload " + workloadID + ": [" + strings.Join(newNamespaces, ", ") + "]")
	
	w.logger.Info("workload-monitoring", "📝 Workload namespaces added to client-side filtering - comprehensive monitoring already active")
	
	// Create namespace-scoped informers for namespace GVRs
	if len(w.cmdNamespaceGVRs) > 0 {
		w.createNamespaceScopedInformers(newNamespaces, workloadID)
	}
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
	var newNamespaces []string
	
	// Find truly new namespaces
	for _, ns := range namespaces {
		if !w.monitoredNamespaces[ns] {
			w.monitoredNamespaces[ns] = true
			w.namespaceToWorkloadID[ns] = workloadID
			newNamespaces = append(newNamespaces, ns)
		}
	}
	w.mu.Unlock()
	
	if len(newNamespaces) == 0 {
		w.logger.Info("workload-monitoring", "No new namespaces found for workload "+workloadID)
		return
	}
	
	w.logger.Info("workload-monitoring", 
		"["+w.clusterName+"] 📋 Found " + strconv.Itoa(len(newNamespaces)) + " NEW namespaces for workload " + workloadID + ": [" + strings.Join(newNamespaces, ", ") + "]")
	
	// With client-side filtering, no need to add new configs - existing informers will capture events
	w.logger.Info("workload-monitoring", "✅ New namespaces will be monitored by existing informers with client-side filtering")
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

// createNamespaceScopedInformers creates per-namespace informers for namespace GVRs
func (w *WorkloadMonitor) createNamespaceScopedInformers(namespaces []string, workloadID string) {
	var resourceConfigs []faro.ResourceConfig
	
	// Create one ResourceConfig per GVR with ALL namespaces for proper server-side filtering
	for _, gvr := range w.cmdNamespaceGVRs {
			scope := w.determineGVRScope(gvr, w.discoverAllNamespacedGVRs())
			resourceConfig := faro.ResourceConfig{
				GVR:               gvr,
				Scope:             scope,
			NamespacePatterns: namespaces, // Server-side filtering for ALL namespaces
			}
		resourceConfigs = append(resourceConfigs, resourceConfig)
			
			w.logger.Info("workload-informers", 
			"["+w.clusterName+"] 📊 Creating namespace-scoped informer: "+gvr+" for namespaces ["+strings.Join(namespaces, ", ")+"] (workload: "+workloadID+")")
	}
	
	// Add all configs at once
	w.sharedController.AddResources(resourceConfigs)
	
	// Restart informers to pick up new namespace-scoped configurations
	if err := w.sharedController.RestartInformers(); err != nil {
		w.logger.Error("workload-informers", "Failed to restart informers for namespace-scoped GVRs: "+err.Error())
	} else {
		w.logger.Info("workload-informers", 
			"["+w.clusterName+"] ✅ Added "+strconv.Itoa(len(w.cmdNamespaceGVRs))+" namespace-scoped informers for "+strconv.Itoa(len(namespaces))+" namespaces")
	}
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
	detectionLabel := flag.String("workload-label", "app.kubernetes.io/name", "Label key to detect workloads (e.g., 'app.kubernetes.io/name')")
	workloadPattern := flag.String("workload-pattern", ".*", "Regex pattern to match workload names")
	workloadIDPattern := flag.String("workload-id-pattern", "{workload-id}.*", "Pattern to extract workload ID from namespace names (use {workload-id} as placeholder)")
	clusterGVRsFlag := flag.String("clustergvrs", "", "Comma-separated list of cluster-scoped GVRs to monitor (e.g., v1/namespaces)")
	namespaceGVRsFlag := flag.String("namespacegvrs", "", "Comma-separated list of namespace-scoped GVRs to create per-namespace informers for detected workloads")
	flag.Parse()

	// Capture full command line for logging
	commandLine := strings.Join(os.Args, " ")

	// Compile the regex pattern
	namePattern, err := regexp.Compile(*workloadPattern)
	if err != nil {
		log.Fatalf("Invalid regex pattern '%s': %v", *workloadPattern, err)
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
	logger, err := faro.NewLogger(logDir)
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	// Log startup messages with full context
	logger.Info("startup", "🚀 Workload Monitor Starting (Scalable Version)")
	logger.Info("startup", "🏛️  Cluster: "+detectedClusterName)
	logger.Info("startup", "💻 Command: "+commandLine)
	logger.Info("startup", "🏷️  Detection label: "+*detectionLabel)
	logger.Info("startup", "📋 Workload pattern: "+*workloadPattern)
	logger.Info("startup", "📁 Workload ID pattern: "+*workloadIDPattern)
	if *clusterGVRsFlag != "" {
		logger.Info("startup", "🌐 Cluster GVRs: "+*clusterGVRsFlag)
	}
	if *namespaceGVRsFlag != "" {
		logger.Info("startup", "📋 Namespace GVRs (per-namespace): "+*namespaceGVRsFlag)
	}

	// Parse GVR lists from command line
	var cmdClusterGVRs, cmdNamespaceGVRs []string
	if *clusterGVRsFlag != "" {
		cmdClusterGVRs = strings.Split(*clusterGVRsFlag, ",")
		for i, gvr := range cmdClusterGVRs {
			cmdClusterGVRs[i] = strings.TrimSpace(gvr)
		}
	}
	if *namespaceGVRsFlag != "" {
		cmdNamespaceGVRs = strings.Split(*namespaceGVRsFlag, ",")
		for i, gvr := range cmdNamespaceGVRs {
			cmdNamespaceGVRs[i] = strings.TrimSpace(gvr)
		}
	}

	// Create monitor
	monitor := &WorkloadMonitor{
		client:                   client,
		logger:                   logger,
		detectionLabel:           *detectionLabel,
		workloadNamePattern:      namePattern,
		workloadIDPattern:        *workloadIDPattern,
		logDir:                   logDir,
		clusterName:              detectedClusterName,
		commandLine:              commandLine,
		detectedWorkloads:        make(map[string]bool),
		monitoredNamespaces:      make(map[string]bool),
		namespaceToWorkloadID:    make(map[string]string),
		workloadIDToWorkloadName: make(map[string]string),
		cmdClusterGVRs:           cmdClusterGVRs,
		cmdNamespaceGVRs:         cmdNamespaceGVRs,
	}
	
	// Start comprehensive monitoring immediately with cluster/namespace GVR filtering
	logger.Info("startup", "🔧 Setting up comprehensive monitoring with cluster/namespace GVR filtering")
	
	// Discover all GVRs (both namespaced and cluster-scoped)
	allNamespacedGVRs := monitor.discoverAllNamespacedGVRs()
	allClusterScopedGVRs := monitor.discoverAllClusterScopedGVRs()
	allGVRs := append(allNamespacedGVRs, allClusterScopedGVRs...)
	
	// Apply cluster/namespace GVR filtering
	filteredGVRs := monitor.filterGVRs(allGVRs)
	excludedCount := len(allGVRs) - len(filteredGVRs)
	
	logger.Info("startup", 
		"📊 Discovered " + strconv.Itoa(len(allNamespacedGVRs)) + " namespaced + " + strconv.Itoa(len(allClusterScopedGVRs)) + " cluster-scoped = " + strconv.Itoa(len(allGVRs)) + " total GVRs → filtered to " + strconv.Itoa(len(filteredGVRs)) + " (excluded: " + strconv.Itoa(excludedCount) + ")")
	
	// Create comprehensive config with filtered GVRs
	var resourceConfigs []faro.ResourceConfig
	for _, gvr := range filteredGVRs {
		scope := monitor.determineGVRScope(gvr, allNamespacedGVRs)
		resourceConfigs = append(resourceConfigs, faro.ResourceConfig{
			GVR:   gvr,
			Scope: scope,
			// No NamespacePatterns = watch ALL namespaces, filter client-side
		})
	}
	
	comprehensiveConfig := &faro.Config{
		OutputDir: logDir,
		LogLevel:  "info",
		Resources: resourceConfigs,
	}

	// Create shared controller with comprehensive monitoring
	controller := faro.NewController(client, logger, comprehensiveConfig)
	monitor.sharedController = controller

	// Register the monitor as an event handler
	controller.AddEventHandler(monitor)

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

	logger.Info("startup", "✅ Comprehensive workload monitor started with " + strconv.Itoa(len(resourceConfigs)) + " filtered informers")
	logger.Info("startup", "🔍 Ready for workload detection and client-side filtering")

	// Start the controller in a goroutine
	go func() {
		if err := controller.Start(); err != nil {
			logger.Error("startup", "Controller failed: "+err.Error())
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Info("shutdown", "✅ Scalable workload monitor stopped gracefully")
}