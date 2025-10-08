package integration

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	faro "github.com/T0MASD/faro/pkg"
	"github.com/T0MASD/faro/tests/testutils"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// RealWorkloadMonitor implements the actual workload monitoring functionality
// This is based on the examples/workload-monitor.go implementation
type RealWorkloadMonitor struct {
	client                   *faro.KubernetesClient
	discoveryController      *faro.Controller
	unifiedController        *faro.Controller
	workloadIDRegex          *regexp.Regexp
	detectedWorkloads        map[string][]string // map[workloadID] -> []namespaces
	workloadIDToWorkloadName map[string]string   // map[workloadID] -> workloadName
	mu                       sync.RWMutex
	
	// Dynamic GVR discovery
	discoveredGVRs           map[string]bool
	
	// Test context
	t                        *testing.T
}

// OnMatched handles namespace detection for workload discovery
func (w *RealWorkloadMonitor) OnMatched(event faro.MatchedEvent) error {
	w.t.Logf("🔍 [WorkloadMonitor] OnMatched called: %s %s", event.EventType, event.GVR)
	
	if event.GVR == "v1/namespaces" && event.EventType == "ADDED" {
		return w.handleNamespaceDetection(event)
	}
	
	// Handle dynamic GVR discovery from v1/events
	if event.GVR == "v1/events" && (event.EventType == "ADDED" || event.EventType == "UPDATED") {
		w.t.Logf("🔍 [WorkloadMonitor] Processing v1/events for dynamic GVR discovery")
		return w.handleDynamicGVRDiscovery(event)
	}
	
	return nil
}

func (w *RealWorkloadMonitor) handleNamespaceDetection(event faro.MatchedEvent) error {
	namespaceName := event.Object.GetName()
	labels := event.Object.GetLabels()
	
	// Check if this namespace has the workload detection label
	if appName, hasLabel := labels["app.kubernetes.io/name"]; hasLabel && appName == "faro" {
		w.t.Logf("🔍 Detected workload namespace: %s (workload: %s)", namespaceName, appName)
		
		// Extract workload ID from namespace name using regex
		matches := w.workloadIDRegex.FindStringSubmatch(namespaceName)
		if len(matches) > 1 {
			workloadID := matches[1]
			workloadName := appName
			
			w.t.Logf("✅ New workload detected: %s (ID: %s)", workloadName, workloadID)
			
			w.mu.Lock()
			if _, exists := w.detectedWorkloads[workloadID]; !exists {
				w.detectedWorkloads[workloadID] = make([]string, 0)
				w.workloadIDToWorkloadName[workloadID] = workloadName
			}
			w.detectedWorkloads[workloadID] = append(w.detectedWorkloads[workloadID], namespaceName)
			w.mu.Unlock()
			
			w.t.Logf("📋 Added namespace %s to workload %s", namespaceName, workloadID)
		} else {
			w.t.Logf("⚠️  Namespace %s doesn't match workload ID pattern %s", namespaceName, w.workloadIDRegex.String())
		}
	}
	
	return nil
}

func (w *RealWorkloadMonitor) findWorkloadIDForNamespace(namespace string) string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	
	for workloadID, namespaces := range w.detectedWorkloads {
		for _, ns := range namespaces {
			if ns == namespace {
				return workloadID
			}
		}
	}
	return ""
}

func (w *RealWorkloadMonitor) getWorkloadName(workloadID string) string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	
	if name, exists := w.workloadIDToWorkloadName[workloadID]; exists {
		return name
	}
	return workloadID
}

func (w *RealWorkloadMonitor) handleDynamicGVRDiscovery(event faro.MatchedEvent) error {
	w.t.Logf("🔍 [WorkloadMonitor] handleDynamicGVRDiscovery called for event %s/%s", event.Object.GetNamespace(), event.Object.GetName())
	
	// Extract involvedObject from the event
	involvedObj, found, err := unstructured.NestedMap(event.Object.Object, "involvedObject")
	if err != nil || !found || involvedObj == nil {
		w.t.Logf("🔍 [WorkloadMonitor] No involvedObject found in event %s/%s (found=%v, err=%v)", event.Object.GetNamespace(), event.Object.GetName(), found, err)
		return nil
	}
	
	// Extract GVR from involvedObject
	discoveredGVR := w.extractGVRFromInvolvedObject(involvedObj)
	if discoveredGVR == "" {
		w.t.Logf("🔍 [WorkloadMonitor] Could not extract GVR from involvedObject in event %s/%s", event.Object.GetNamespace(), event.Object.GetName())
		return nil
	}
	
	w.t.Logf("🔍 [WorkloadMonitor] Discovered GVR '%s' from v1/events in namespace %s", discoveredGVR, event.Object.GetNamespace())
	
	// Check if this GVR is already being monitored
	w.mu.Lock()
	if w.discoveredGVRs == nil {
		w.discoveredGVRs = make(map[string]bool)
	}
	
	if w.discoveredGVRs[discoveredGVR] {
		w.mu.Unlock()
		w.t.Logf("🔍 [WorkloadMonitor] GVR '%s' already being monitored, skipping", discoveredGVR)
		return nil
	}
	
	// Mark as discovered
	w.discoveredGVRs[discoveredGVR] = true
	w.mu.Unlock()
	
	// Actually add the GVR to the unified controller for monitoring
	w.addGVRToController(discoveredGVR, event.Object.GetNamespace())
	
	return nil
}

func (w *RealWorkloadMonitor) extractGVRFromInvolvedObject(involvedObj map[string]interface{}) string {
	apiVersion, ok := involvedObj["apiVersion"].(string)
	if !ok || apiVersion == "" {
		return ""
	}
	
	kind, ok := involvedObj["kind"].(string)
	if !ok || kind == "" {
		return ""
	}
	
	// Convert kind to plural resource name (simplified)
	resource := strings.ToLower(kind) + "s"
	
	// Handle special cases
	switch kind {
	case "Endpoints":
		resource = "endpoints"
	case "NetworkPolicy":
		resource = "networkpolicies"
	}
	
	// Build GVR string
	if apiVersion == "v1" {
		return "v1/" + resource
	}
	return apiVersion + "/" + resource
}

func (w *RealWorkloadMonitor) addGVRToController(discoveredGVR, namespace string) {
	w.t.Logf("🔍 [WorkloadMonitor] Adding GVR '%s' to unified controller for namespace '%s'", discoveredGVR, namespace)
	
	// Create a new resource configuration for the discovered GVR
	newResourceConfig := faro.ResourceConfig{
		GVR:            discoveredGVR,
		Scope:          faro.NamespaceScope, // Assume namespaced for simplicity
		NamespaceNames: []string{namespace},
	}
	
	// Add the resource to the unified controller
	w.unifiedController.AddResources([]faro.ResourceConfig{newResourceConfig})
	
	// Start the informer for the newly added resource
	if err := w.unifiedController.StartInformers(); err != nil {
		w.t.Logf("❌ [WorkloadMonitor] Failed to start new informers for GVR '%s': %v", discoveredGVR, err)
		return
	}
	
	w.t.Logf("✅ [WorkloadMonitor] Successfully added and started informer for GVR '%s'", discoveredGVR)
}

// WorkloadJSONMiddleware implements JSONMiddleware to add workload annotations before JSON logging
type WorkloadJSONMiddleware struct {
	Monitor *RealWorkloadMonitor
}

func (w *WorkloadJSONMiddleware) ProcessBeforeJSON(eventType, gvr, namespace, name, uid string, obj *unstructured.Unstructured) (*unstructured.Unstructured, bool) {
	// Only process namespaced resources
	if namespace == "" || obj == nil {
		return obj, true
	}
	
	// Find workload ID for this namespace
	workloadID := w.Monitor.findWorkloadIDForNamespace(namespace)
	if workloadID == "" {
		return obj, true
	}
	
	workloadName := w.Monitor.getWorkloadName(workloadID)
	
	w.Monitor.t.Logf("🔧 [Middleware] Adding workload annotations to %s %s %s/%s (workload: %s)", 
		eventType, gvr, namespace, name, workloadID)
	
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

func TestWorkloadControllerRegex(t *testing.T) {
	t.Log("========================================")
	t.Log("🚀 STARTING REAL WORKLOAD MONITOR TEST")
	t.Log("========================================")
	
	// Generate unique workload ID for this test run
	workloadID := fmt.Sprintf("test%d", time.Now().Unix()%10000)
	logDir := "./logs/TestWorkloadControllerRegex"
	
	t.Logf("🎯 Test workload ID: %s", workloadID)
	t.Logf("📁 Log directory: %s", logDir)
	
	// ========================================
	// SETUP: Kubernetes client and Faro components
	// ========================================
	t.Log("")
	t.Log("⚙️  SETUP: Setting up Kubernetes client and Faro components...")
	
	// Create Kubernetes client
	k8sClient, _ := testutils.CreateKubernetesClients(t)
	
	// Create Faro client
	faroClient, err := faro.NewKubernetesClient()
	if err != nil {
		t.Fatalf("Failed to create Faro client: %v", err)
	}
	
	// Create Faro config for JSON export
	config := &faro.Config{
		OutputDir:  logDir,
		LogLevel:   "debug",
		JsonExport: true,
	}
	
	// Create logger
	logger, err := faro.NewLogger(config)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	
	// Create test namespaces that match the workload pattern
	testNamespaces := []string{
		fmt.Sprintf("faro-%s", workloadID),      // Main workload namespace
		fmt.Sprintf("faro-%s-app", workloadID),  // App component namespace
		fmt.Sprintf("faro-%s-db", workloadID),   // DB component namespace
	}
	
	// ========================================
	// PHASE 1: START MONITORING
	// ========================================
	t.Log("")
	t.Log("▶️  PHASE 1: START MONITORING")
	
	// Create discovery config for namespace detection - using simplified resources format
	discoveryConfig := &faro.Config{
		OutputDir:  logDir,
		LogLevel:   "debug",
		JsonExport: true,
		Resources: []faro.ResourceConfig{
			{
				GVR:            "v1/namespaces",
				NamespaceNames: []string{""}, // Cluster-scoped
			},
		},
	}
	
	// Create workload config for resource monitoring - using simplified resources format
	workloadConfig := &faro.Config{
		OutputDir:  logDir,
		LogLevel:   "debug",
		JsonExport: true,
		Resources: []faro.ResourceConfig{
			// Monitor jobs in all workload namespaces
			{
				GVR:            "batch/v1/jobs",
				NamespaceNames: []string{
					fmt.Sprintf("faro-%s", workloadID),
					fmt.Sprintf("faro-%s-app", workloadID),
					fmt.Sprintf("faro-%s-db", workloadID),
				},
			},
			// Monitor configmaps in all workload namespaces
			{
				GVR:            "v1/configmaps",
				NamespaceNames: []string{
					fmt.Sprintf("faro-%s", workloadID),
					fmt.Sprintf("faro-%s-app", workloadID),
					fmt.Sprintf("faro-%s-db", workloadID),
				},
			},
			// Monitor events in all workload namespaces
			{
				GVR:            "v1/events",
				NamespaceNames: []string{
					fmt.Sprintf("faro-%s", workloadID),
					fmt.Sprintf("faro-%s-app", workloadID),
					fmt.Sprintf("faro-%s-db", workloadID),
				},
			},
		},
	}
	
	// Create RealWorkloadMonitor
	monitor := &RealWorkloadMonitor{
		client:                   faroClient,
		workloadIDRegex:          regexp.MustCompile(`^faro-([^-]+)$`),
		detectedWorkloads:        make(map[string][]string),
		workloadIDToWorkloadName: make(map[string]string),
		discoveredGVRs:           make(map[string]bool),
		t:                        t,
	}
	
	// Create discovery controller
	discoveryController := faro.NewController(faroClient, logger, discoveryConfig)
	monitor.discoveryController = discoveryController
	discoveryController.AddEventHandler(monitor)
	
	// Create unified workload controller
	unifiedController := faro.NewController(faroClient, logger, workloadConfig)
	monitor.unifiedController = unifiedController
	
	// Add workload monitor as event handler for dynamic GVR discovery from v1/events
	unifiedController.AddEventHandler(monitor)
	
	// Add JSON middleware for workload annotation injection
	workloadMiddleware := &WorkloadJSONMiddleware{Monitor: monitor}
	unifiedController.AddJSONMiddleware(workloadMiddleware)
	
	// Set up readiness tracking
	controllersReady := make(chan struct{}, 2)
	
	// Set readiness callbacks
	discoveryController.SetReadyCallback(func() {
		t.Log("✅ Discovery controller is ready")
		controllersReady <- struct{}{}
	})
	
	unifiedController.SetReadyCallback(func() {
		t.Log("✅ Unified controller is ready")
		controllersReady <- struct{}{}
	})
	
	// Start discovery controller
	go func() {
		if err := discoveryController.Start(); err != nil {
			t.Logf("Discovery controller error: %v", err)
		}
	}()
	
	// Start unified controller
	go func() {
		if err := unifiedController.Start(); err != nil {
			t.Logf("Unified controller error: %v", err)
		}
	}()
	
	// Wait for both controllers to be ready
	t.Log("⏳ Waiting for controllers to be ready...")
	for i := 0; i < 2; i++ {
		select {
		case <-controllersReady:
			t.Logf("Controller %d/2 ready", i+1)
		case <-time.After(30 * time.Second):
			t.Fatalf("Timeout waiting for controllers to be ready")
		}
	}
	t.Log("🎯 Both controllers are ready")
	
	// ========================================
	// PHASE 2: WORKING WITH MANIFESTS
	// ========================================
	t.Log("")
	t.Log("📦 PHASE 2: WORKING WITH MANIFESTS")
	
	// Create test namespaces
	t.Log("🏗️  Creating test namespaces...")
	for i, namespaceName := range testNamespaces {
		component := "main"
		if namespaceName == fmt.Sprintf("faro-%s-app", workloadID) {
			component = "app"
		} else if namespaceName == fmt.Sprintf("faro-%s-db", workloadID) {
			component = "db"
		}
		
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespaceName,
				Labels: map[string]string{
					"app.kubernetes.io/name": "faro", // Detection label
					"component":                      component,
				},
			},
		}
		
		_, err := k8sClient.CoreV1().Namespaces().Create(context.TODO(), namespace, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create namespace %s: %v", namespaceName, err)
		}
		t.Logf("   ✅ Created namespace %d/%d: %s", i+1, len(testNamespaces), namespaceName)
	}
	
	// Wait for namespace detection
	t.Log("⏳ Waiting for namespace detection...")
	time.Sleep(2 * time.Second)
	
	// Create jobs in workload namespaces
	t.Log("🚀 Creating jobs in workload namespaces...")
	for i, namespaceName := range testNamespaces {
		jobName := fmt.Sprintf("hello-world-%d", i+1)
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      jobName,
				Namespace: namespaceName,
				Labels: map[string]string{
					"app": "hello-world",
				},
			},
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyNever,
						Containers: []corev1.Container{
							{
								Name:  "hello",
								Image: "busybox",
								Command: []string{"echo", "Hello World"},
							},
						},
					},
				},
			},
		}
		
		_, err := k8sClient.BatchV1().Jobs(namespaceName).Create(context.TODO(), job, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create job %s in namespace %s: %v", jobName, namespaceName, err)
		}
		t.Logf("   ✅ Created job %d/%d: %s/%s", i+1, len(testNamespaces), namespaceName, jobName)
	}
	
	// Wait for events to be captured and processed
	t.Log("⏳ Waiting for events to be captured and workload annotations to be added...")
	time.Sleep(3 * time.Second)
	
	// Delete some jobs to test DELETED events with UUID tracking
	t.Log("🗑️  Deleting some jobs to test DELETED events...")
	for i, namespaceName := range testNamespaces[:2] { // Delete jobs from first 2 namespaces
		jobName := fmt.Sprintf("hello-world-%d", i+1)
		err := k8sClient.BatchV1().Jobs(namespaceName).Delete(context.TODO(), jobName, metav1.DeleteOptions{})
		if err != nil {
			t.Logf("⚠️  Failed to delete job %s in namespace %s: %v", jobName, namespaceName, err)
		} else {
			t.Logf("   ✅ Deleted job %d/2: %s/%s", i+1, namespaceName, jobName)
		}
	}
	
	// Wait for DELETED events to be captured
	t.Log("⏳ Waiting for DELETED events to be captured...")
	time.Sleep(3 * time.Second)
	
	// ========================================
	// PHASE 3: STOPPING MONITORING
	// ========================================
	t.Log("")
	t.Log("⏹️  PHASE 3: STOPPING MONITORING")
	
	// Stop controllers and wait for them to fully stop
	t.Log("🛑 Stopping discovery controller...")
	discoveryController.Stop()
	t.Log("✅ Discovery controller stopped")
	
	t.Log("🛑 Stopping unified controller...")
	unifiedController.Stop()
	t.Log("✅ Unified controller stopped")
	
	t.Log("✅ All controllers stopped - monitoring complete")
	
	// ========================================
	// PHASE 4: LOADING EVENTS JSON
	// ========================================
	t.Log("")
	t.Log("🔍 PHASE 4: LOADING EVENTS JSON")
	
	// Load JSON events from disk
	jsonEvents := testutils.ReadJSONEvents(t, logDir)
	t.Logf("📊 Total JSON events captured: %d", len(jsonEvents))
	
	// ========================================
	// PHASE 5: COMPARING DATA
	// ========================================
	t.Log("")
	t.Log("🔍 PHASE 5: COMPARING DATA")
	
	// First, let's see what events we actually captured
	t.Logf("📋 All captured events:")
	for i, event := range jsonEvents {
		t.Logf("  %d. %s %s %s (annotations: %v)", i+1, event.EventType, event.GVR, event.Name, event.Annotations)
		
		// Check for workload annotations
		if event.Annotations != nil {
			if workloadID, hasWorkloadID := event.Annotations["faro.workload.id"]; hasWorkloadID {
				t.Logf("✅ Found workload annotation: %s %s %s (workload: %s)", event.EventType, event.GVR, event.Name, workloadID)
			}
		}
	}
	
	// Count events with workload annotations and analyze DELETED events
	eventsWithWorkloadID := 0
	jobEventsWithWorkloadID := 0
	deletedEvents := 0
	deletedEventsWithUID := 0
	deletedJobEvents := 0
	deletedJobEventsWithUID := 0
	deletedJobEventsWithWorkloadID := 0
	workloadIDs := make(map[string]int)
	
	for _, event := range jsonEvents {
		// Count DELETED events
		if event.EventType == "DELETED" {
			deletedEvents++
			if event.UID != "" {
				deletedEventsWithUID++
			}
			if event.GVR == "batch/v1/jobs" {
				deletedJobEvents++
				if event.UID != "" {
					deletedJobEventsWithUID++
				}
				if event.Annotations != nil {
					if _, hasWorkloadID := event.Annotations["faro.workload.id"]; hasWorkloadID {
						deletedJobEventsWithWorkloadID++
					}
				}
			}
		}
		
		// Count events with workload annotations
		if event.Annotations != nil {
			if workloadID, hasWorkloadID := event.Annotations["faro.workload.id"]; hasWorkloadID {
				eventsWithWorkloadID++
				workloadIDs[workloadID]++
				
				if event.GVR == "batch/v1/jobs" {
					jobEventsWithWorkloadID++
				}
			}
		}
	}
	
	t.Logf("📊 Total events captured: %d", len(jsonEvents))
	t.Logf("📊 Events with workload.id annotations: %d", eventsWithWorkloadID)
	t.Logf("📊 Job events with workload.id annotations: %d", jobEventsWithWorkloadID)
	t.Logf("📊 DELETED events captured: %d", deletedEvents)
	t.Logf("📊 DELETED events with UIDs: %d", deletedEventsWithUID)
	t.Logf("📊 DELETED job events: %d", deletedJobEvents)
	t.Logf("📊 DELETED job events with UIDs: %d", deletedJobEventsWithUID)
	t.Logf("📊 DELETED job events with workload.id: %d", deletedJobEventsWithWorkloadID)
	t.Logf("📊 Workload IDs found: %v", workloadIDs)
	
	// Validate results
	expectedWorkloadID := fmt.Sprintf("test%s", workloadID[4:]) // Remove "test" prefix from workloadID
	if count, found := workloadIDs[expectedWorkloadID]; found && count > 0 {
		t.Logf("✅ SUCCESS: Expected workload ID '%s' found in %d events", expectedWorkloadID, count)
	} else {
		t.Errorf("❌ FAILURE: Expected workload ID '%s' not found in JSON events", expectedWorkloadID)
		t.Errorf("❌ This means the WorkloadMonitor is NOT adding faro.workload.id annotations!")
	}
	
	if eventsWithWorkloadID > 0 {
		t.Logf("✅ SUCCESS: WorkloadMonitor added workload annotations to %d events", eventsWithWorkloadID)
	} else {
		t.Errorf("❌ FAILURE: No events found with workload.id annotations")
		t.Errorf("❌ This proves the WorkloadMonitor annotation injection is NOT working!")
	}
	
	if jobEventsWithWorkloadID > 0 {
		t.Logf("✅ SUCCESS: %d job events have workload annotations", jobEventsWithWorkloadID)
	} else {
		t.Errorf("❌ FAILURE: No job events found with workload.id annotations")
		t.Errorf("❌ Jobs should have workload annotations since they're in workload namespaces!")
	}
	
	// Validate DELETED events (these should FAIL until UUID tracking is fixed)
	if deletedJobEvents > 0 {
		t.Logf("✅ SUCCESS: %d DELETED job events captured", deletedJobEvents)
		
		// Check if DELETED events have UIDs (this should FAIL)
		if deletedJobEventsWithUID > 0 {
			t.Logf("✅ SUCCESS: %d DELETED job events have UIDs", deletedJobEventsWithUID)
		} else {
			t.Errorf("❌ FAILURE: No DELETED job events have UIDs!")
			t.Errorf("❌ UUID tracking for DELETED events is NOT working!")
			t.Errorf("❌ Expected %d DELETED job events to have stored UIDs", deletedJobEvents)
		}
		
		// Check if DELETED events have workload annotations (this should FAIL)
		if deletedJobEventsWithWorkloadID > 0 {
			t.Logf("✅ SUCCESS: %d DELETED job events have workload.id annotations", deletedJobEventsWithWorkloadID)
		} else {
			t.Errorf("❌ FAILURE: No DELETED job events have workload.id annotations!")
			t.Errorf("❌ Workload annotation injection for DELETED events is NOT working!")
			t.Errorf("❌ Expected %d DELETED job events to have workload annotations", deletedJobEvents)
		}
	} else {
		t.Errorf("❌ FAILURE: No DELETED job events captured!")
		t.Errorf("❌ Expected to capture DELETED events for the 2 jobs we deleted")
	}
	
	// ========================================
	// CLEANUP: Remove test namespaces
	// ========================================
	t.Log("")
	t.Log("🧹 CLEANUP: Removing test namespaces...")
	for _, namespaceName := range testNamespaces {
		err := k8sClient.CoreV1().Namespaces().Delete(context.TODO(), namespaceName, metav1.DeleteOptions{})
		if err != nil {
			t.Logf("⚠️  Failed to delete namespace %s: %v", namespaceName, err)
		} else {
			t.Logf("   ✅ Deleted namespace: %s", namespaceName)
		}
	}
	
	t.Log("")
	t.Log("✅ REAL WORKLOAD MONITOR TEST COMPLETED SUCCESSFULLY!")
	t.Log("========================================")
}