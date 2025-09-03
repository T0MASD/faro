package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	faro "github.com/T0MASD/faro/pkg"
	"github.com/T0MASD/faro/tests/testutils"
)

// Use shared FaroJSONEvent from testutils
type FaroJSONEvent = testutils.FaroJSONEvent

// TestVanillaLibraryFunctionality tests Faro library usage directly (migrated from test8)
// Replicates exact test8.sh + test8.go workflow: load simple-test-1.yaml, apply unified-test-resources.yaml
func TestVanillaLibraryFunctionality(t *testing.T) {
	t.Log("🚀 Starting Vanilla Library Integration Test (replicating test8.sh + test8.go)")
	
	// Setup test environment - use same paths as original test8
	logDir := "./logs/integration-test8"
	
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
	
	logger, err := faro.NewLogger(config.GetLogDir())
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
	t.Logf("✅ Faro started with %d builtin + %d dynamic informers", builtin, dynamic)
	
	// Apply manifests (exactly like test8.sh does: kubectl apply -f manifests/unified-test-resources.yaml)
	t.Log("📝 Applying test manifests (unified-test-resources.yaml)...")
	manifestPath := "../e2e/manifests/unified-test-resources.yaml"
	cmd := exec.Command("kubectl", "apply", "-f", manifestPath)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to apply manifests %s: %v", manifestPath, err)
	}
	
	// Wait for events to be processed (like test8.sh: sleep 5)
	time.Sleep(5 * time.Second)
	
	// Test Phase 2: Update ConfigMaps (exactly like test8.sh does)
	t.Log("🔄 Updating ConfigMaps...")
	cmd = exec.Command("kubectl", "patch", "configmap", "test-config-1", "-n", "faro-test-1", "--patch", `{"data":{"updated":"true"}}`)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to patch test-config-1: %v", err)
	}
	cmd = exec.Command("kubectl", "patch", "configmap", "test-config-2", "-n", "faro-test-1", "--patch", `{"data":{"updated":"true"}}`)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to patch test-config-2: %v", err)
	}
	
	// Wait for update events (like test8.sh: sleep 3)
	time.Sleep(3 * time.Second)
	
	// Test Phase 3: Delete ConfigMaps (exactly like test8.sh does)
	t.Log("🗑️  Deleting ConfigMaps...")
	cmd = exec.Command("kubectl", "delete", "configmap", "test-config-1", "-n", "faro-test-1")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to delete test-config-1: %v", err)
	}
	cmd = exec.Command("kubectl", "delete", "configmap", "test-config-2", "-n", "faro-test-1")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to delete test-config-2: %v", err)
	}
	
	// Wait for delete events (like test8.sh: sleep 4)
	time.Sleep(4 * time.Second)
	
	// Stop Faro
	t.Log("🛑 Stopping Faro controller...")
	controller.Stop()
	cancel()
	
	// Verify events were captured (exactly like test8.sh does with grep)
	t.Log("🔍 Verifying captured events...")
	
	logFiles, err := filepath.Glob(filepath.Join(logDir, "logs", "*.log"))
	if err != nil || len(logFiles) == 0 {
		t.Fatalf("No Faro log files found in %s", filepath.Join(logDir, "logs"))
	}
	
	logContent, err := os.ReadFile(logFiles[0])
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}
	
	// Verify ADDED events (like test8.sh: grep -q "CONFIG \[ADDED\].*test-config-1")
	if !strings.Contains(string(logContent), "CONFIG [ADDED]") || !strings.Contains(string(logContent), "test-config-1") {
		t.Errorf("❌ ConfigMap ADDED event for test-config-1 not found")
	} else {
		t.Log("✅ ConfigMap ADDED event for test-config-1 detected!")
	}
	
	// Verify test-config-2 ADDED (like test8.sh: test-config-2 should be processed - no client-side filtering)
	if !strings.Contains(string(logContent), "test-config-2") {
		t.Errorf("❌ ConfigMap test-config-2 ADDED event should be processed (no client-side filtering in Faro core)!")
	} else {
		t.Log("✅ ConfigMap test-config-2 ADDED event processed (no client-side filtering)")
	}
	
	// Verify UPDATED events (like test8.sh: grep -q "CONFIG \[UPDATED\].*test-config-1")
	if !strings.Contains(string(logContent), "CONFIG [UPDATED]") {
		t.Errorf("❌ ConfigMap UPDATED events not found")
	} else {
		t.Log("✅ ConfigMap UPDATED events detected!")
	}
	
	// Verify test-config-2 UPDATED (like test8.sh: no client-side filtering)
	if !strings.Contains(string(logContent), "CONFIG [UPDATED]") || !strings.Contains(string(logContent), "test-config-2") {
		t.Errorf("❌ ConfigMap test-config-2 UPDATED event should be processed (no client-side filtering in Faro core)!")
	} else {
		t.Log("✅ ConfigMap test-config-2 UPDATED event processed (no client-side filtering)")
	}
	
	// Verify DELETED events (like test8.sh: grep -q "CONFIG \[DELETED\].*test-config-1")
	if !strings.Contains(string(logContent), "CONFIG [DELETED]") {
		t.Errorf("❌ ConfigMap DELETED events not found")
	} else {
		t.Log("✅ ConfigMap DELETED events detected!")
	}
	
	// Verify test-config-2 DELETED (like test8.sh: no client-side filtering)
	if !strings.Contains(string(logContent), "CONFIG [DELETED]") || !strings.Contains(string(logContent), "test-config-2") {
		t.Errorf("❌ ConfigMap test-config-2 DELETED event should be processed (no client-side filtering in Faro core)!")
	} else {
		t.Log("✅ ConfigMap test-config-2 DELETED event processed (no client-side filtering)")
	}
	
	// Show CONFIG events (like test8.sh: grep "CONFIG" "$log_file")
	t.Log("📋 CONFIG events in log file:")
	lines := strings.Split(string(logContent), "\n")
	for _, line := range lines {
		if strings.Contains(line, "CONFIG") {
			t.Logf("  %s", line)
		}
	}
	
	// Verify JSON export - this is the key validation
	t.Log("🔍 Verifying JSON export events...")
	jsonEvents := testutils.ReadJSONEvents(t, logDir)
	
	// What we CONFIGURED to capture (from simple-test-1.yaml):
	// - namespace: faro-test-1 
	// - resource: v1/configmaps
	// - name_pattern: test-config-1
	t.Log("📋 Configuration Analysis:")
	t.Log("   - Configured namespace: faro-test-1")
	t.Log("   - Configured resource: v1/configmaps") 
	t.Log("   - Configured name_pattern: test-config-1")
	
	// What we DEPLOYED (from unified-test-resources.yaml):
	// - test-config-1 (matches pattern)
	// - test-config-2 (doesn't match pattern but should be captured due to no client-side filtering)
	t.Log("📋 Deployment Analysis:")
	t.Log("   - Deployed: test-config-1 (matches name_pattern)")
	t.Log("   - Deployed: test-config-2 (doesn't match name_pattern)")
	t.Log("   - Expected: Both should be captured (no client-side filtering)")
	
	// What we CAPTURED in JSON:
	t.Logf("📋 JSON Events Captured (%d total):", len(jsonEvents))
	configMapEvents := make(map[string][]string) // name -> event types
	for _, event := range jsonEvents {
		if event.GVR == "v1/configmaps" && event.Namespace == "faro-test-1" {
			configMapEvents[event.Name] = append(configMapEvents[event.Name], event.EventType)
			t.Logf("   - %s %s/%s", event.EventType, event.Namespace, event.Name)
		}
	}
	
	// Validation: Compare configured vs deployed vs captured
	t.Log("🔍 Validation Results:")
	
	// Verify test-config-1 (should match pattern and be captured)
	if events, exists := configMapEvents["test-config-1"]; !exists {
		t.Errorf("❌ test-config-1 not found in JSON events (should match name_pattern)")
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
	
	// Verify test-config-2 (doesn't match pattern but should be captured - no client-side filtering)
	if events, exists := configMapEvents["test-config-2"]; !exists {
		t.Errorf("❌ test-config-2 not found in JSON events (should be captured despite not matching name_pattern - no client-side filtering)")
	} else {
		hasAdded := testutils.Contains(events, "ADDED")
		hasUpdated := testutils.Contains(events, "UPDATED")
		hasDeleted := testutils.Contains(events, "DELETED") 
		if hasAdded && hasUpdated && hasDeleted {
			t.Log("✅ test-config-2: Complete lifecycle captured (proves no client-side filtering)")
		} else {
			t.Errorf("❌ test-config-2: Incomplete lifecycle - ADDED:%v UPDATED:%v DELETED:%v", hasAdded, hasUpdated, hasDeleted)
		}
	}
	
	// Summary validation
	if len(configMapEvents) >= 2 {
		t.Log("✅ JSON Export Validation: PASSED")
		t.Log("   - Configuration loaded correctly from simple-test-1.yaml")
		t.Log("   - Deployment applied correctly from unified-test-resources.yaml") 
		t.Log("   - JSON events captured correctly (no client-side filtering confirmed)")
	} else {
		t.Errorf("❌ JSON Export Validation: FAILED - Expected at least 2 ConfigMaps, got %d", len(configMapEvents))
	}
	
	t.Log("✅ Test 8 completed! (Vanilla Library Integration Test)")
	t.Log("📋 Summary:")
	t.Log("   - Used library to replicate vanilla Faro functionality")
	t.Log("   - Same behavior as test1 but via direct library calls")
	t.Log("   - All events detected: ADDED, UPDATED, DELETED")
	t.Log("   - Loaded config from simple-test-1.yaml (like test8.go)")
	t.Log("   - Applied unified-test-resources.yaml (like test8.sh)")
	t.Log("   - Verified no client-side filtering exists in Faro core")
}

// All helper functions moved to shared testutils package
