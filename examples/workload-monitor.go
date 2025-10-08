package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	faro "github.com/T0MASD/faro/pkg"
)

// WorkloadDetector implements business logic for workload detection and management
// This demonstrates how library users implement policies using Faro mechanisms
// DYNAMIC APPROACH: Configured via command-line parameters, not hardcoded
type WorkloadDetector struct {
	client                   *faro.KubernetesClient
	logger                   *faro.Logger
	discoveryController      *faro.Controller  // For namespace discovery
	workloadController       *faro.Controller  // For workload resource monitoring
	mu                       sync.RWMutex
	
	// Dynamic configuration (from command line parameters)
	detectionLabel           string            // e.g., "app.kubernetes.io/name"
	workloadNamePattern      *regexp.Regexp    // e.g., "faro"
	workloadIDExtractor      string            // e.g., "env-staging-(.+)"
	clusterGVRs              []string          // e.g., ["v1/namespaces"]
	namespaceGVRs            []string          // e.g., ["v1/configmaps", "batch/v1/jobs"]
	
	// State management (user responsibility)
	detectedWorkloads        map[string]WorkloadInfo
	dynamicGVRs              map[string]bool
	initialGVRs              map[string]bool
}

// WorkloadInfo contains workload metadata managed by business logic
type WorkloadInfo struct {
	ID         string
	Name       string
	Namespaces []string
	// Resources are determined dynamically, not stored here
}

// WorkloadEvent represents a business event (user-defined structure)
type WorkloadEvent struct {
	Timestamp    time.Time         `json:"timestamp"`
	WorkloadID   string            `json:"workload_id"`
	WorkloadName string            `json:"workload_name"`
	Action       string            `json:"action"`
	Resource     string            `json:"resource"`
	Namespace    string            `json:"namespace"`
	Name         string            `json:"name"`
	Labels       map[string]string `json:"labels,omitempty"`
}

// NewWorkloadDetector creates a new workload detector with dynamic configuration
func NewWorkloadDetector(client *faro.KubernetesClient, logger *faro.Logger, 
	detectionLabel, workloadPattern, idExtractor string, 
	clusterGVRs, namespaceGVRs []string) (*WorkloadDetector, error) {
	
	namePattern, err := regexp.Compile(workloadPattern)
	if err != nil {
		return nil, fmt.Errorf("invalid workload pattern: %v", err)
	}
	
	detector := &WorkloadDetector{
		client:              client,
		logger:              logger,
		detectionLabel:      detectionLabel,
		workloadNamePattern: namePattern,
		workloadIDExtractor: idExtractor,
		clusterGVRs:         clusterGVRs,
		namespaceGVRs:       namespaceGVRs,
		detectedWorkloads:   make(map[string]WorkloadInfo),
		dynamicGVRs:         make(map[string]bool),
		initialGVRs:         make(map[string]bool),
	}
	
	// Track initially configured GVRs
	for _, gvr := range namespaceGVRs {
		detector.initialGVRs[gvr] = true
	}
	
	return detector, nil
}

// Start initializes the workload detection system
func (wd *WorkloadDetector) Start() error {
	// Phase 1: Set up discovery controller (Faro mechanism)
	// Dynamic configuration based on command-line parameters
	var discoveryResources []faro.ResourceConfig
	
	// Add cluster-scoped GVRs if specified
	for _, gvr := range wd.clusterGVRs {
		discoveryResources = append(discoveryResources, faro.ResourceConfig{
			GVR:   gvr,
			Scope: faro.ClusterScope,
		})
	}
	
	// Always add v1/namespaces for workload detection
	discoveryResources = append(discoveryResources, faro.ResourceConfig{
		GVR:   "v1/namespaces",
		Scope: faro.ClusterScope,
		// No label selector - we filter in business logic
	})
	
	discoveryConfig := &faro.Config{
		OutputDir:  "./logs/discovery",
		LogLevel:   "info",
		JsonExport: true,
		Resources:  discoveryResources,
	}
	
	wd.discoveryController = faro.NewController(wd.client, wd.logger, discoveryConfig)
	wd.discoveryController.AddEventHandler(&NamespaceDiscoveryHandler{detector: wd})
	
	// Start discovery controller (Faro mechanism)
	if err := wd.discoveryController.Start(); err != nil {
		return fmt.Errorf("failed to start discovery controller: %v", err)
	}
	
	wd.logger.Info("workload-detector", "✅ Workload detector started - discovery phase active")
	return nil
}

// Stop gracefully shuts down the workload detector
func (wd *WorkloadDetector) Stop() {
	if wd.discoveryController != nil {
		wd.discoveryController.Stop()
	}
	if wd.workloadController != nil {
		wd.workloadController.Stop()
	}
	wd.logger.Info("workload-detector", "✅ Workload detector stopped")
}

// NamespaceDiscoveryHandler implements business logic for namespace-based workload discovery
type NamespaceDiscoveryHandler struct {
	detector *WorkloadDetector
}

func (h *NamespaceDiscoveryHandler) OnMatched(event faro.MatchedEvent) error {
	// Business logic: Handle ALL namespace events for continuous workload discovery
	if event.GVR != "v1/namespaces" || event.EventType != "ADDED" {
		return nil
	}

	namespaceName := event.Object.GetName()
	labels := event.Object.GetLabels()
	
	// Check 1: Does this namespace have the detection label? (New workload detection)
	if workloadName, exists := labels[h.detector.detectionLabel]; exists {
		// Business policy: Pattern matching for new workload
		if h.detector.workloadNamePattern.MatchString(workloadName) {
			h.detector.logger.Info("workload-discovery", 
				fmt.Sprintf("🔍 Discovered NEW workload namespace: %s (workload: %s)", namespaceName, workloadName))
			
			// Extract workload ID and register new workload
			workloadID := h.extractWorkloadID(namespaceName, workloadName)
			return h.detector.registerNewWorkload(workloadID, workloadName, namespaceName)
		}
		return nil
	}
	
	// Check 2: Does this namespace match any existing workload? (Continuous discovery)
	return h.checkExistingWorkloads(namespaceName)
}

// extractWorkloadID implements business logic for ID extraction
func (h *NamespaceDiscoveryHandler) extractWorkloadID(namespaceName, workloadName string) string {
	// Business policy: Extract ID from namespace name
	if h.detector.workloadIDExtractor != "" {
		if pattern, err := regexp.Compile(h.detector.workloadIDExtractor); err == nil {
			if matches := pattern.FindStringSubmatch(namespaceName); len(matches) > 1 {
				return matches[1]
			}
		}
	}
	
	// Fallback: Use workload name as ID
	return workloadName
}

// checkExistingWorkloads checks if a namespace belongs to any existing workload
func (h *NamespaceDiscoveryHandler) checkExistingWorkloads(namespaceName string) error {
	h.detector.mu.RLock()
	
	// Check if this namespace matches any existing workload ID
	for workloadID, workloadInfo := range h.detector.detectedWorkloads {
		if strings.Contains(namespaceName, workloadID) {
			h.detector.logger.Info("workload-discovery", 
				fmt.Sprintf("🔍 Found namespace %s matching existing workload %s", namespaceName, workloadID))
			
			// Check if already in the workload
			alreadyExists := false
			for _, existingNS := range workloadInfo.Namespaces {
				if existingNS == namespaceName {
					alreadyExists = true
					break
				}
			}
			
			h.detector.mu.RUnlock() // Release read lock before calling add function
			
			if alreadyExists {
				return nil // Already tracked
			}
			
			// Add to existing workload
			return h.detector.addNamespaceToWorkload(workloadID, namespaceName)
		}
	}
	
	h.detector.mu.RUnlock()
	return nil // No match found
}

// registerNewWorkload registers a new workload with its first namespace
func (wd *WorkloadDetector) registerNewWorkload(workloadID, workloadName, namespaceName string) error {
	wd.mu.Lock()
	defer wd.mu.Unlock()
	
	// Check if workload already exists
	if _, exists := wd.detectedWorkloads[workloadID]; exists {
		wd.logger.Info("workload-registration", 
			fmt.Sprintf("🔄 Workload %s already exists", workloadID))
		return nil
	}
	
	// Create new workload
	wd.detectedWorkloads[workloadID] = WorkloadInfo{
		ID:         workloadID,
		Name:       workloadName,
		Namespaces: []string{namespaceName},
	}
	
	wd.logger.Info("workload-registration", 
		fmt.Sprintf("✅ Registered NEW workload: %s (%s) with namespace: %s", workloadID, workloadName, namespaceName))
	
	// Start monitoring - create controller for first namespace
	return wd.startInitialMonitoring(workloadID, namespaceName)
}

// startInitialMonitoring creates the workload controller for the first namespace
func (wd *WorkloadDetector) startInitialMonitoring(workloadID, namespaceName string) error {
	// Create resource configurations for this namespace
	var resourceConfigs []faro.ResourceConfig
	for _, gvr := range wd.namespaceGVRs {
		resourceConfigs = append(resourceConfigs, faro.ResourceConfig{
			GVR:            gvr,
			Scope:          faro.NamespaceScope,
			NamespaceNames: []string{namespaceName},
		})
	}
	
	// Create workload controller
	workloadConfig := &faro.Config{
		OutputDir:  fmt.Sprintf("./logs/workload-%s", workloadID),
		LogLevel:   "info",
		JsonExport: true,
		Resources:  resourceConfigs,
	}
	
	wd.workloadController = faro.NewController(wd.client, wd.logger, workloadConfig)
	wd.workloadController.AddEventHandler(&WorkloadResourceHandler{
		detector:     wd,
		workloadID:   workloadID,
		workloadName: wd.detectedWorkloads[workloadID].Name,
	})
	
	// Start workload controller
	if err := wd.workloadController.Start(); err != nil {
		return fmt.Errorf("failed to start workload controller: %v", err)
	}
	
	wd.logger.Info("workload-monitoring", 
		fmt.Sprintf("🚀 Started workload controller for: %s with namespace: %s", workloadID, namespaceName))
	
	return nil
}

// addNamespaceToWorkload adds a namespace to an existing workload (thread-safe)
func (wd *WorkloadDetector) addNamespaceToWorkload(workloadID, namespaceName string) error {
	wd.mu.Lock()
	defer wd.mu.Unlock()
	
	// Get existing workload
	workloadInfo, exists := wd.detectedWorkloads[workloadID]
	if !exists {
		wd.logger.Warning("workload-monitoring", 
			fmt.Sprintf("❌ Workload %s not found when trying to add namespace %s", workloadID, namespaceName))
		return fmt.Errorf("workload %s not found", workloadID)
	}
	
	// Add namespace to workload
	workloadInfo.Namespaces = append(workloadInfo.Namespaces, namespaceName)
	wd.detectedWorkloads[workloadID] = workloadInfo
	
	wd.logger.Info("workload-monitoring", 
		fmt.Sprintf("🚀 Added namespace %s to workload %s, starting monitoring", namespaceName, workloadID))
	
	// Start monitoring for this specific namespace - SIMPLIFIED
	if wd.workloadController != nil {
		// Create resource configurations for this namespace
		var resourceConfigs []faro.ResourceConfig
		for _, gvr := range wd.namespaceGVRs {
			resourceConfigs = append(resourceConfigs, faro.ResourceConfig{
				GVR:            gvr,
				Scope:          faro.NamespaceScope,
				NamespaceNames: []string{namespaceName},
			})
		}
		
		// Add resources to existing controller
		wd.workloadController.AddResources(resourceConfigs)
		
		// Start informers for the newly added resources
		if err := wd.workloadController.StartInformers(); err != nil {
			wd.logger.Error("workload-monitoring", 
				fmt.Sprintf("Failed to start informers for namespace %s: %v", namespaceName, err))
			return err
		}
		
		wd.logger.Info("workload-monitoring", 
			fmt.Sprintf("✅ Added monitoring for namespace %s to existing controller", namespaceName))
	} else {
		wd.logger.Warning("workload-monitoring", 
			fmt.Sprintf("⚠️ No workload controller exists to add namespace %s", namespaceName))
	}
	
	return nil
}

// startNamespaceMonitoring starts resource monitoring for a specific namespace in a workload
func (wd *WorkloadDetector) startNamespaceMonitoring(workloadID, namespaceName string) error {
	// Create resource configurations for this namespace
	var resourceConfigs []faro.ResourceConfig
	for _, gvr := range wd.namespaceGVRs {
		resourceConfigs = append(resourceConfigs, faro.ResourceConfig{
			GVR:            gvr,
			Scope:          faro.NamespaceScope,
			NamespaceNames: []string{namespaceName},
		})
	}
	
	// Add resources to existing controller or create new one
	if wd.workloadController == nil {
		// Create workload controller using Faro mechanisms
		workloadConfig := &faro.Config{
			OutputDir:  fmt.Sprintf("./logs/workload-%s", workloadID),
			LogLevel:   "info",
			JsonExport: true,
			Resources:  resourceConfigs,
		}
		
		wd.workloadController = faro.NewController(wd.client, wd.logger, workloadConfig)
		wd.workloadController.AddEventHandler(&WorkloadResourceHandler{
			detector:     wd,
			workloadID:   workloadID,
			workloadName: wd.detectedWorkloads[workloadID].Name,
		})
		
		// Start workload controller (Faro mechanism)
		if err := wd.workloadController.Start(); err != nil {
			return fmt.Errorf("failed to start workload controller: %v", err)
		}
		
		wd.logger.Info("workload-monitoring", 
			fmt.Sprintf("🚀 Started NEW workload controller for: %s", workloadID))
	} else {
		// Add resources to existing controller
		wd.workloadController.AddResources(resourceConfigs)
		wd.logger.Info("workload-monitoring", 
			fmt.Sprintf("🔄 Added resources for namespace %s to existing controller", namespaceName))
	}
	
	return nil
}

// registerWorkload implements business logic for workload registration
func (wd *WorkloadDetector) registerWorkload(workloadID, workloadName string, namespaces []string) error {
	wd.mu.Lock()
	defer wd.mu.Unlock()
	
	// Business logic: Track workload state
	wd.detectedWorkloads[workloadID] = WorkloadInfo{
		ID:         workloadID,
		Name:       workloadName,
		Namespaces: namespaces,
		// Resources are determined dynamically from command-line parameters
	}
	
	wd.logger.Info("workload-registration", 
		fmt.Sprintf("✅ Registered workload: %s (%s)", workloadID, workloadName))
	
	// Business logic: Set up workload monitoring using Faro mechanisms
	return wd.setupWorkloadMonitoring(workloadID)
}

// setupWorkloadMonitoring uses Faro mechanisms to monitor workload resources
func (wd *WorkloadDetector) setupWorkloadMonitoring(workloadID string) error {
	workloadInfo := wd.detectedWorkloads[workloadID]
	
	// Business logic: Create resource configurations for this workload
	// DYNAMIC: Use command-line specified GVRs, not hardcoded ones
	var resourceConfigs []faro.ResourceConfig
	for _, gvr := range wd.namespaceGVRs {
		for _, namespace := range workloadInfo.Namespaces {
			resourceConfigs = append(resourceConfigs, faro.ResourceConfig{
				GVR:               gvr,
				Scope:             faro.NamespaceScope,
				NamespaceNames: []string{namespace},
				// Simple configuration - complex filtering done in business logic
			})
		}
	}
	
	// Create workload controller using Faro mechanisms
	workloadConfig := &faro.Config{
		OutputDir:  fmt.Sprintf("./logs/workload-%s", workloadID),
		LogLevel:   "info",
		JsonExport: true,
		Resources:  resourceConfigs,
	}
	
	// Create new controller or update existing one
	if wd.workloadController == nil {
		wd.workloadController = faro.NewController(wd.client, wd.logger, workloadConfig)
		wd.workloadController.AddEventHandler(&WorkloadResourceHandler{
			detector:     wd,
			workloadID:   workloadID,
			workloadName: workloadInfo.Name,
		})
		
		// Start workload controller (Faro mechanism)
		if err := wd.workloadController.Start(); err != nil {
			return fmt.Errorf("failed to start workload controller: %v", err)
		}
	} else {
		// Business logic: Add resources to existing controller
		for _, config := range resourceConfigs {
			wd.workloadController.AddResources([]faro.ResourceConfig{config})
		}
	}
	
	wd.logger.Info("workload-monitoring", 
		fmt.Sprintf("🚀 Started monitoring workload: %s", workloadID))
	
	return nil
}

// WorkloadResourceHandler implements business logic for workload resource events
type WorkloadResourceHandler struct {
	detector     *WorkloadDetector
	workloadID   string
	workloadName string
}

func (h *WorkloadResourceHandler) OnMatched(event faro.MatchedEvent) error {
	// Business logic: Process workload resource events
	workloadEvent := WorkloadEvent{
		Timestamp:    time.Now(),
		WorkloadID:   h.workloadID,
		WorkloadName: h.workloadName,
		Action:       event.EventType,
		Resource:     event.GVR,
		Namespace:    event.Object.GetNamespace(),
		Name:         event.Object.GetName(),
		Labels:       event.Object.GetLabels(),
	}
	
	// Business logic: Custom event processing
	if err := h.processWorkloadEvent(workloadEvent); err != nil {
		return fmt.Errorf("failed to process workload event: %v", err)
	}
	
	// Business logic: Dynamic GVR discovery from events
	if event.GVR == "v1/events" {
		h.handleDynamicGVRDiscovery(event)
	}
	
	return nil
}

// processWorkloadEvent implements business logic for event processing
func (h *WorkloadResourceHandler) processWorkloadEvent(event WorkloadEvent) error {
	// Business logic: Log structured workload event
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return err
	}
	
	h.detector.logger.Info("workload-event", string(eventJSON))
	
	// Business logic: Additional processing (notifications, workflows, etc.)
	// This is where users implement their specific business requirements
	
	return nil
}

// handleDynamicGVRDiscovery implements business logic for dynamic resource discovery
func (h *WorkloadResourceHandler) handleDynamicGVRDiscovery(event faro.MatchedEvent) {
	// Business logic: Extract GVR from Kubernetes events
	if involvedObj, ok := event.Object.Object["involvedObject"].(map[string]interface{}); ok {
		if discoveredGVR := h.extractGVRFromEvent(involvedObj); discoveredGVR != "" {
			h.detector.mu.Lock()
			if !h.detector.dynamicGVRs[discoveredGVR] {
				h.detector.dynamicGVRs[discoveredGVR] = true
				h.detector.logger.Info("dynamic-discovery", 
					fmt.Sprintf("🔍 Discovered new GVR: %s for workload %s", discoveredGVR, h.workloadID))
				
				// Business logic: Add discovered GVR to monitoring
				go h.addDiscoveredGVR(discoveredGVR)
			}
			h.detector.mu.Unlock()
		}
	}
}

// extractGVRFromEvent implements business logic for GVR extraction
func (h *WorkloadResourceHandler) extractGVRFromEvent(involvedObj map[string]interface{}) string {
	apiVersion, ok1 := involvedObj["apiVersion"].(string)
	kind, ok2 := involvedObj["kind"].(string)
	
	if !ok1 || !ok2 {
		return ""
	}
	
	// CORRECTED: The Kubernetes API provides correct resource information
	// We don't need special cases - the API is always correct
	// Simple pluralization works for most cases, and if it doesn't,
	// the API server will reject invalid GVRs anyway
	resource := strings.ToLower(kind) + "s"
	
	return apiVersion + "/" + resource
}

// addDiscoveredGVR uses Faro mechanisms to add dynamically discovered resources
func (h *WorkloadResourceHandler) addDiscoveredGVR(gvr string) {
	workloadInfo := h.detector.detectedWorkloads[h.workloadID]
	
	// Business logic: Create resource configs for discovered GVR
	for _, namespace := range workloadInfo.Namespaces {
		config := faro.ResourceConfig{
			GVR:               gvr,
			Scope:             faro.NamespaceScope,
			NamespaceNames: []string{namespace},
		}
		
		// Use Faro mechanism to add resource
		h.detector.workloadController.AddResources([]faro.ResourceConfig{config})
		h.detector.logger.Info("dynamic-discovery", 
			fmt.Sprintf("✅ Added discovered GVR %s to workload %s", gvr, h.workloadID))
	}
}

func main() {
	fmt.Println("🚀 Clean Workload Monitor - Demonstrating Mechanisms vs Policies")
	fmt.Println("📚 Faro Core: Provides informer management, event streaming, JSON export")
	fmt.Println("🔧 User Code: Implements workload detection, business logic, workflows")
	
	// Parse command line flags - DYNAMIC configuration like original
	discoverNamespaces := flag.String("discover-namespaces", "app.kubernetes.io/name~.*", 
		"Find namespaces by label key and pattern (format: 'label-key~pattern')")
	extractFromNamespace := flag.String("extract-from-namespace", "env-staging-(.+)", 
		"Regex pattern to extract workload identifier from main namespace names")
	clusterResources := flag.String("cluster-resources", "", 
		"Comma-separated list of cluster-scoped GVRs to monitor (e.g., v1/namespaces)")
	namespaceResources := flag.String("namespace-resources", "v1/configmaps,batch/v1/jobs,v1/events", 
		"Comma-separated list of namespace-scoped GVRs to create per-namespace informers")
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warning, error, fatal)")
	flag.Parse()

	// Parse discover-namespaces flag (format: "label-key~pattern")
	var detectionLabel, workloadPattern string
	if strings.Contains(*discoverNamespaces, "~") {
		parts := strings.SplitN(*discoverNamespaces, "~", 2)
		detectionLabel = parts[0]
		workloadPattern = parts[1]
	} else {
		log.Fatalf("Invalid discover-namespaces format '%s'. Expected format: 'label-key~pattern'", *discoverNamespaces)
	}

	// Parse GVR lists from command line - DYNAMIC like original
	var clusterGVRs, namespaceGVRs []string
	if *clusterResources != "" {
		clusterGVRs = strings.Split(*clusterResources, ",")
		for i, gvr := range clusterGVRs {
			clusterGVRs[i] = strings.TrimSpace(gvr)
		}
	}
	if *namespaceResources != "" {
		namespaceGVRs = strings.Split(*namespaceResources, ",")
		for i, gvr := range namespaceGVRs {
			namespaceGVRs[i] = strings.TrimSpace(gvr)
		}
	}
	
	// Create Kubernetes client (Faro mechanism)
	client, err := faro.NewKubernetesClient()
	if err != nil {
		log.Fatalf("Failed to create Kubernetes client: %v", err)
	}
	
	// Create logger (Faro mechanism)
	loggerConfig := &faro.Config{OutputDir: "./logs", LogLevel: *logLevel, JsonExport: true}
	logger, err := faro.NewLogger(loggerConfig)
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	// Log startup with dynamic configuration
	logger.Info("startup", "🔍 Discover namespaces: "+*discoverNamespaces)
	logger.Info("startup", "🏷️  Detection label: "+detectionLabel)
	logger.Info("startup", "📋 Workload pattern: "+workloadPattern)
	logger.Info("startup", "📁 Extract from namespace: "+*extractFromNamespace)
	if len(clusterGVRs) > 0 {
		logger.Info("startup", "🌐 Cluster resources: "+strings.Join(clusterGVRs, ", "))
	}
	if len(namespaceGVRs) > 0 {
		logger.Info("startup", "📋 Namespace resources: "+strings.Join(namespaceGVRs, ", "))
	}
	
	// Create workload detector with DYNAMIC configuration (Business logic)
	detector, err := NewWorkloadDetector(
		client,
		logger,
		detectionLabel,           // From command line
		workloadPattern,          // From command line
		*extractFromNamespace,    // From command line
		clusterGVRs,             // From command line
		namespaceGVRs,           // From command line
	)
	if err != nil {
		log.Fatalf("Failed to create workload detector: %v", err)
	}
	
	// Start workload detection (Business logic using Faro mechanisms)
	if err := detector.Start(); err != nil {
		log.Fatalf("Failed to start workload detector: %v", err)
	}
	
	logger.Info("startup", "✅ Clean workload monitor started with dynamic configuration")
	logger.Info("startup", fmt.Sprintf("🔍 Monitoring for workloads with label: %s~%s", detectionLabel, workloadPattern))
	logger.Info("startup", "📡 Create namespaces with matching labels to trigger workload detection")
	
	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	
	fmt.Println("\n🛑 Shutting down...")
	detector.Stop()
	fmt.Println("✅ Clean workload monitor stopped")
}