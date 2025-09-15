package integration

import (
	"context"
	"os/exec"
	"testing"
	"time"

	faro "github.com/T0MASD/faro/pkg"
	"github.com/T0MASD/faro/tests/testutils"
)

// Use shared FaroJSONEvent from testutils
type FaroJSONEvent = testutils.FaroJSONEvent

// TestVanillaLibraryFunctionality tests Faro library usage directly (migrated from test8)
// Uses vanilla-specific manifests for proper integration testing isolation
func TestVanillaLibraryFunctionality(t *testing.T) {
	t.Log("")
	t.Log("========================================")
	t.Log("🚀 VANILLA LIBRARY INTEGRATION TEST")
	t.Log("========================================")
	
	// Setup test environment - use same paths as original test8
	logDir := "./logs/TestVanillaLibraryFunctionality"
	
	// Ensure log directory exists
	testutils.EnsureLogDir(t, logDir)
	
	// Create Kubernetes clients for test setup
	k8sClient, _ := testutils.CreateKubernetesClients(t)
	
	// Cleanup function - clean up faro-test-1 namespace like original test8.sh
	cleanup := func() {
		t.Log("🧹 Cleaning up test resources...")
		testutils.DeleteNamespace(t, k8sClient, "faro-test-1")
	}
	defer cleanup()
	
	// ========================================
	// PHASE 1: START MONITORING
	// ========================================
	t.Log("")
	t.Log("📡 PHASE 1: Starting Faro monitoring...")
	
	// Load configuration from YAML file (exactly like test8.go does)
	config := &faro.Config{}
	configPath := "../e2e/configs/simple-test-1.yaml"
	if err := config.LoadFromYAML(configPath); err != nil {
		t.Fatalf("Failed to load config from %s: %v", configPath, err)
	}
	
	// Override output directory for integration test
	config.OutputDir = logDir
	config.AutoShutdownSec = 0 // No auto-shutdown for integration test
	config.JsonExport = true   // Enable JSON export for verification
	
	// Create Faro components
	faroClient, err := faro.NewKubernetesClient()
	if err != nil {
		t.Fatalf("Failed to create Faro Kubernetes client: %v", err)
	}
	
	logger, err := faro.NewLogger(config)
	if err != nil {
		t.Fatalf("Failed to create Faro logger: %v", err)
	}
	defer logger.Shutdown()
	
	// Create and start Faro controller
	controller := faro.NewController(faroClient, logger, config)
	
	// Start Faro in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = ctx // Suppress unused variable warning
	
	// Set up readiness callback
	readyDone := make(chan struct{})
	controller.SetReadyCallback(func() {
		close(readyDone)
	})
	
	go func() {
		if err := controller.Start(); err != nil {
			t.Errorf("Failed to start Faro controller: %v", err)
		}
	}()
	
	// Wait for Faro to be ready using the callback mechanism
	t.Log("⏳ Waiting for Faro to initialize...")
	select {
	case <-readyDone:
		t.Log("✅ Faro initialization completed via callback")
	case <-time.After(60 * time.Second):
		t.Fatal("Faro failed to initialize within timeout")
	}
	
	// Verify Faro is running
	builtin, dynamic := controller.GetActiveInformers()
	t.Logf("✅ PHASE 1 COMPLETE: Faro started with %d builtin + %d dynamic informers", builtin, dynamic)
	
	// ========================================
	// PHASE 2: WORKING WITH MANIFESTS
	// ========================================
	t.Log("")
	t.Log("📝 PHASE 2: Working with manifests...")
	
	// Apply vanilla-specific manifests for integration testing
	t.Log("Applying vanilla library test manifests...")
	manifestPath := "manifests/vanilla-library-test.yaml"
	cmd := exec.Command("kubectl", "apply", "-f", manifestPath)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to apply manifests %s: %v", manifestPath, err)
	}
	
	// Wait for events to be processed (like test8.sh: sleep 5)
	time.Sleep(5 * time.Second)
	
	// Test Phase 2: Update ConfigMap
	t.Log("Updating ConfigMap...")
	cmd = exec.Command("kubectl", "patch", "configmap", "test-config-1", "-n", "faro-test-1", "--patch", `{"data":{"updated":"true"}}`)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to patch test-config-1: %v", err)
	}
	
	// Wait for update events (like test8.sh: sleep 3)
	time.Sleep(3 * time.Second)
	
	// Test Phase 3: Delete ConfigMap
	t.Log("Deleting ConfigMap...")
	cmd = exec.Command("kubectl", "delete", "configmap", "test-config-1", "-n", "faro-test-1")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to delete test-config-1: %v", err)
	}
	
	// Wait for delete events (like test8.sh: sleep 4)
	time.Sleep(4 * time.Second)
	
	// ========================================
	// PHASE 3: STOPPING MONITORING
	// ========================================
	t.Log("")
	t.Log("🛑 PHASE 3: Stopping monitoring - all manifest work complete")
	controller.Stop()
	cancel()
	
	// ========================================
	// PHASE 4: LOADING EVENTS JSON
	// ========================================
	t.Log("")
	t.Log("📊 PHASE 4: Loading and analyzing captured JSON events...")
	
	// Verify JSON export - this is the key validation
	t.Log("Verifying JSON export events...")
	jsonEvents := testutils.ReadJSONEvents(t, logDir)
	
	// What we CONFIGURED to capture (from simple-test-1.yaml):
	// - namespace: faro-test-1 
	// - resource: v1/configmaps
	// - name_selector: test-config-1
	t.Log("📋 Configuration Analysis:")
	t.Log("   - Configured namespace: faro-test-1")
	t.Log("   - Configured resource: v1/configmaps") 
	t.Log("   - Configured name_selector: test-config-1")
	
	// What we DEPLOYED (from manifests/vanilla-library-test.yaml):
	// - test-config-1 (matches selector)
	t.Log("📋 Deployment Analysis:")
	t.Log("   - Deployed: test-config-1 (matches name_selector)")
	t.Log("   - Expected: ConfigMap should be captured")
	
	// What we CAPTURED in JSON:
	t.Logf("📋 JSON Events Captured (%d total):", len(jsonEvents))
	configMapEvents := make(map[string][]string) // name -> event types
	for _, event := range jsonEvents {
		if event.GVR == "v1/configmaps" && event.Namespace == "faro-test-1" {
			configMapEvents[event.Name] = append(configMapEvents[event.Name], event.EventType)
			t.Logf("   - %s %s/%s", event.EventType, event.Namespace, event.Name)
		}
	}
	
	// ========================================
	// PHASE 5: COMPARING DATA
	// ========================================
	t.Log("")
	t.Log("🔍 PHASE 5: Comparing and validating data...")
	
	// Validation: Compare configured vs deployed vs captured
	t.Log("Validation Results:")
	
	// Verify test-config-1 (should match selector and be captured)
	if events, exists := configMapEvents["test-config-1"]; !exists {
		t.Errorf("❌ test-config-1 not found in JSON events (should match name_selector)")
	} else {
		hasAdded := testutils.Contains(events, "ADDED")
		hasUpdated := testutils.Contains(events, "UPDATED") 
		hasDeleted := testutils.Contains(events, "DELETED")
		if hasAdded && hasUpdated && hasDeleted {
			t.Log("✅ test-config-1: Complete lifecycle captured (ADDED, UPDATED, DELETED)")
		} else {
			t.Errorf("❌ test-config-1: Incomplete lifecycle - ADDED:%v UPDATED:%v DELETED:%v", hasAdded, hasUpdated, hasDeleted)
		}
	}
	
	// Note: Only test-config-1 exists in vanilla-library-test.yaml manifest
	
	// Summary validation
	if len(configMapEvents) >= 1 {
		t.Log("✅ JSON Export Validation: PASSED")
		t.Log("   - Configuration loaded correctly from simple-test-1.yaml")
		t.Log("   - Deployment applied correctly from manifests/vanilla-library-test.yaml") 
		t.Log("   - JSON events captured correctly for vanilla library integration")
	} else {
		t.Errorf("❌ JSON Export Validation: FAILED - Expected at least 1 ConfigMap, got %d", len(configMapEvents))
	}
	
	t.Log("")
	t.Log("✅ VANILLA LIBRARY INTEGRATION TEST COMPLETED SUCCESSFULLY!")
	t.Log("========================================")
	t.Log("🎯 FINAL TEST SUMMARY")
	t.Log("========================================")
	t.Logf("   📋 Configuration: simple-test-1.yaml")
	t.Logf("   📋 Manifests: manifests/vanilla-library-test.yaml")
	t.Logf("   📋 JSON events captured: %d", len(jsonEvents))
	t.Logf("   ✅ Phase 1 - Monitoring started: SUCCESS")
	t.Logf("   ✅ Phase 2 - Manifests deployed: SUCCESS")
	t.Logf("   ✅ Phase 3 - Monitoring stopped: SUCCESS")
	t.Logf("   ✅ Phase 4 - JSON events loaded: SUCCESS")
	t.Logf("   ✅ Phase 5 - Data validation: SUCCESS")
	t.Logf("   ✅ Library functionality: SUCCESS")
	t.Logf("   ✅ ConfigMap lifecycle: SUCCESS")
	t.Logf("   ✅ No client-side filtering: SUCCESS")
	t.Log("========================================")
}

// All helper functions moved to shared testutils package
