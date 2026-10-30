package e2e

import (
	"github.com/T0MASD/faro/tests/testutils"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

var testenv env.Environment

var (
	binaryPath string
	binaryOnce sync.Once
)

// buildBinary builds the Faro binary once and reuses it across all tests
// stopFaro ends the Faro process the way Kubernetes would, with SIGTERM.
//
// Not Process.Kill(): that is SIGKILL, which cannot be caught, so main()'s
// graceful shutdown never runs and the logger never flushes what it has
// buffered. SIGTERM is also what a pod gets on delete, so it is the path worth
// exercising. SIGKILL stays as the backstop if the process will not leave.
func stopFaro(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func buildBinary() error {
	var buildErr error
	binaryOnce.Do(func() {
		// Get absolute paths
		projectRoot, _ := filepath.Abs("../..")
		binaryPath = filepath.Join(projectRoot, "faro-e2e")
		
		cmd := exec.Command("go", "build", "-o", binaryPath, ".")
		cmd.Dir = projectRoot
		
		// Capture output for debugging
		output, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("build failed: %v, output: %s", err, string(output))
			return
		}
		
		// Verify binary was created
		if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
			buildErr = fmt.Errorf("binary was not created at: %s", binaryPath)
		}
	})
	return buildErr
}

// cleanupBinary removes the binary (call this after all tests complete)
func cleanupBinary() {
	if binaryPath != "" {
		os.Remove(binaryPath)
	}
}

// extractLogDirFromConfig reads the output_dir from a config file
func extractLogDirFromConfig(configFile string) string {
	content, err := os.ReadFile(configFile)
	if err != nil {
		return "logs" // fallback
	}
	
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "output_dir:") {
			// Extract the path from 'output_dir: "./logs/test1"'
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				path := strings.TrimSpace(parts[1])
				path = strings.Trim(path, `"`)
				path = strings.TrimPrefix(path, "./")
				return path
			}
		}
	}
	return "logs" // fallback
}


// Shared with every other suite, so the contract has one definition.
type FaroJSONEvent = testutils.FaroJSONEvent





// Manifest resource representation
type ManifestResource struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   ResourceMetadata  `yaml:"metadata"`
}

type ResourceMetadata struct {
	Name      string            `yaml:"name"`
	Namespace string            `yaml:"namespace"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

func TestMain(m *testing.M) {
	testenv = env.New()
	testenv.Setup(
		func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
			cmd := exec.Command("go", "build", "-o", "faro", ".")
			cmd.Dir = "../.."
			return ctx, cmd.Run()
		},
	)
	os.Exit(testenv.Run(m))
}


// countLogLines returns how many lines in Faro's own log for this run contain needle.
func countLogLines(logDir, needle string) (int, error) {
	pattern := filepath.Join(logDir, "logs", "faro-*.log")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			return 0, err
		}
		total += strings.Count(string(content), needle)
	}
	return total, nil
}

// assertNoDroppedEvents fails the test if Faro queued events it could not process.
//
// reconcile() resolves a lister by "<gvr>@<object namespace>". An informer that
// watches every namespace registers its lister as "<gvr>@", so the lookup misses
// and every namespaced event is dropped and requeued. The watch, the queue and
// the event counts all look healthy; only this log line reports it.
func assertNoDroppedEvents(t *testing.T, logDir string) {
	const needle = "No lister found for key"
	n, err := countLogLines(logDir, needle)
	if err != nil {
		t.Logf("could not scan Faro log for dropped events: %v", err)
		return
	}
	if n > 0 {
		t.Errorf("Faro dropped %d queued events (%q in %s/logs/): an informer "+
			"registered a lister under a key reconcile() does not look up, so the "+
			"events were discarded and requeued rather than reported", n, needle, logDir)
		return
	}
	t.Logf("✓ no dropped events in %s/logs/", logDir)
}







// convertToGVR converts apiVersion and kind to GVR format
func convertToGVR(apiVersion, kind string) string {
	// Handle core API resources
	if apiVersion == "v1" {
		switch strings.ToLower(kind) {
		case "namespace":
			return "v1/namespaces"
		case "configmap":
			return "v1/configmaps"
		case "secret":
			return "v1/secrets"
		case "service":
			return "v1/services"
		case "pod":
			return "v1/pods"
		default:
			return fmt.Sprintf("v1/%ss", strings.ToLower(kind))
		}
	}

	// Handle other API groups
	return fmt.Sprintf("%s/%ss", apiVersion, strings.ToLower(kind))
}

// runParallelE2ETest runs an e2e test using existing configs/manifests for parallel execution
func runParallelE2ETest(t *testing.T, testName, configFile, manifestFile, updateManifestFile string, expected expectation) {
	// Build binary (only once across all tests)
	t.Log("Ensuring binary is available...")
	if err := buildBinary(); err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}
	t.Logf("Using binary: %s", binaryPath)
	
	// Create isolated test environment
	testEnv := env.New()
	
	feature := features.New(fmt.Sprintf("Parallel Test %s", testName)).
		Assess("should capture events in parallel", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			runE2ETestWithManifestParallel(t, ctx, cfg, configFile, manifestFile, updateManifestFile, expected)
			return ctx
		}).Feature()
	
	testEnv.Test(t, feature)
}

// runE2ETestWithManifestParallel is the parallel version of runE2ETestWithManifest
func runE2ETestWithManifestParallel(t *testing.T, ctx context.Context, cfg *envconf.Config, configFile, manifestFile, updateManifestFile string, expected expectation) {
	// ========================================
	// PHASE 1: START MONITORING
	// ========================================
	t.Log("")
	t.Log("📡 PHASE 1: Starting Faro binary...")

	// Start Faro with binary and original config
	configPath, _ := filepath.Abs(configFile)
	cmd := exec.Command(binaryPath, "-config", configPath)
	cmd.Dir = "."
	cmd.Env = os.Environ()

	stdout, err := cmd.StdoutPipe()
			if err != nil {
		t.Fatalf("Failed to create stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("Failed to create stderr pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start Faro: %v", err)
	}

	// Ensure Faro is killed even if test fails
	defer func() {
		if cmd.Process != nil {
			stopFaro(cmd)
		}
	}()

	// Wait for Faro to be ready
	if err := waitForFaroReady(t, configFile, cmd, stdout, stderr); err != nil {
		t.Fatalf("Faro failed to start: %v", err)
	}

	t.Log("✅ PHASE 1 COMPLETE: Faro binary is ready!")

	// ========================================
	// PHASE 2: APPLY MANIFESTS
	// ========================================
	t.Log("")
	t.Log("📝 PHASE 2: Working with manifests...")

	// Apply manifest
	t.Log("Applying manifest...")
	manifestPath, _ := filepath.Abs(manifestFile)
	applyCmd := exec.Command("kubectl", "apply", "-f", manifestPath)
	applyCmd.Env = os.Environ()
	if err := applyCmd.Run(); err != nil {
		t.Fatalf("Failed to apply manifest: %v", err)
	}

	// Update ConfigMap if update manifest exists
	if updateManifestFile != "" {
		t.Log("Updating ConfigMap...")
		updatePath, _ := filepath.Abs(updateManifestFile)
		updateCmd := exec.Command("kubectl", "apply", "-f", updatePath)
		updateCmd.Env = os.Environ()
		if err := updateCmd.Run(); err != nil {
			t.Log("ConfigMap update failed (might not exist): " + err.Error())
		}
	}

	// Delete manifest
	t.Log("Deleting manifest...")
	deleteCmd := exec.Command("kubectl", "delete", "-f", manifestPath, "--ignore-not-found")
	deleteCmd.Env = os.Environ()
	if err := deleteCmd.Run(); err != nil {
		t.Logf("Failed to delete manifest: %v", err)
	}

	t.Log("✅ PHASE 2 COMPLETE: All manifest work finished!")

	// ========================================
	// PHASE 3: STOP MONITORING
	// ========================================
	t.Log("")
	t.Log("🛑 PHASE 3: Stopping Faro binary...")

	// Stop Faro
	if cmd.Process != nil {
		stopFaro(cmd)
	}

	t.Log("✅ PHASE 3 COMPLETE: Faro binary stopped!")

	// ========================================
	// PHASE 4: ANALYZE RESULTS
	// ========================================
	t.Log("")
	t.Log("📊 PHASE 4: Loading and analyzing captured JSON events...")

	// Extract log directory from config file
	logDir := extractLogDirFromConfig(configFile)
	
	// Read events from log directory
	events, err := readJSONEvents(logDir)
	if err != nil {
		t.Fatalf("Failed to read events: %v", err)
	}

	t.Log("=== LOG LOCATIONS ===")
	t.Logf("Faro logs: %s/logs/", logDir)
	
	// Find JSON export file
	jsonPattern := filepath.Join(logDir, "logs", "events-*.json")
	jsonFiles, err := filepath.Glob(jsonPattern)
	if err == nil && len(jsonFiles) > 0 {
		t.Logf("JSON export: %s", jsonFiles[len(jsonFiles)-1])
	} else {
		t.Logf("JSON export: Not found")
	}

	t.Log("✅ PHASE 4 COMPLETE: JSON events loaded successfully!")

	// ========================================
	// PHASE 5: VALIDATE RESULTS
	// ========================================
	t.Log("")
	t.Log("🔍 PHASE 5: Comparing and validating data...")

	assertNoDroppedEvents(t, logDir)
	validateExpectation(t, expected, events)

	t.Log("✅ PHASE 5 COMPLETE: Data validation finished!")
	t.Log("")
	t.Log("========================================")
	t.Log("🎯 E2E TEST SUMMARY")
	t.Log("========================================")
	t.Log("   ✅ Phase 1 - Faro binary started: SUCCESS")
	t.Log("   ✅ Phase 2 - Manifests processed: SUCCESS")
	t.Log("   ✅ Phase 3 - Faro binary stopped: SUCCESS")
	t.Log("   ✅ Phase 4 - JSON events loaded: SUCCESS")
	t.Log("   ✅ Phase 5 - Data validation: SUCCESS")
	t.Log("========================================")
}

func TestFaroTest1NamespaceCentric(t *testing.T) {
	t.Parallel() // Enable parallel execution

	// The config names one ConfigMap, test-config-1. The namespace also holds
	// the cluster's kube-root-ca.crt and the update's Secret, and a decoy
	// ConfigMap with another name: none of them may be captured.
	const cfg, create, update = "configs/simple-test-1.yaml", "manifests/test1-manifest.yaml", "manifests/test1-manifest-update.yaml"
	runParallelE2ETest(t, "test1", cfg, create, update, mustExpect(t, cfg, create, update))
}

func TestFaroTest2ResourceCentric(t *testing.T) {
	t.Parallel() // Enable parallel execution

	// Test 2 config specifies name_selector: "test-config-1", so only test-config-1 should be captured
	const cfg, create, update = "configs/simple-test-2.yaml", "manifests/test2-manifest.yaml", "manifests/test2-manifest-update.yaml"
	expected := handWritten(t, []FaroJSONEvent{
		{EventType: "ADDED", GVR: "v1/configmaps", Namespace: "faro-test-2", Name: "test-config-1"},
		{EventType: "UPDATED", GVR: "v1/configmaps", Namespace: "faro-test-2", Name: "test-config-1"},
		{EventType: "DELETED", GVR: "v1/configmaps", Namespace: "faro-test-2", Name: "test-config-1"},
	}, cfg, create, update)

	runParallelE2ETest(t, "test2", cfg, create, update, expected)
}

func runE2ETestWithManifest(t *testing.T, ctx context.Context, cfg *envconf.Config, configFile string, manifestFile string, logDir string, expected expectation) {
	// ========================================
	// PHASE 1: START MONITORING
	// ========================================
	t.Log("")
	t.Log("📡 PHASE 1: Starting Faro binary...")
	
	// Start Faro
	faroCmd := exec.CommandContext(ctx, "../../faro", "-config", configFile)
	faroCmd.Env = os.Environ()
	
	// Capture stdout and stderr to monitor initialization
	stdout, err := faroCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("Failed to get stdout pipe: %v", err)
	}
	stderr, err := faroCmd.StderrPipe()
	if err != nil {
		t.Fatalf("Failed to get stderr pipe: %v", err)
	}
	
	if err := faroCmd.Start(); err != nil {
		t.Fatalf("Failed to start Faro: %v", err)
	}
	defer func() {
		if faroCmd.Process != nil {
			stopFaro(faroCmd)
		}
	}()

	// Wait for Faro to be ready by monitoring its logs
	if err := waitForFaroReady(t, configFile, faroCmd, stdout, stderr); err != nil {
		t.Fatalf("Faro failed to initialize: %v", err)
	}
	
	t.Log("✅ PHASE 1 COMPLETE: Faro binary is ready!")

	// ========================================
	// PHASE 2: WORKING WITH MANIFESTS
	// ========================================
	t.Log("")
	t.Log("📝 PHASE 2: Working with manifests...")
	
	// Apply manifest
	t.Log("Applying manifest...")
	applyCmd := exec.Command("kubectl", "apply", "-f", manifestFile)
	applyCmd.Env = os.Environ()
	if err := applyCmd.Run(); err != nil {
		t.Fatalf("Failed to apply manifest: %v", err)
	}
	defer func() {
		// Clean up resources using update manifest (backward compatible)
		updateManifestFile := strings.Replace(manifestFile, ".yaml", "-update.yaml", 1)
		deleteCmd := exec.Command("kubectl", "delete", "-f", updateManifestFile, "--ignore-not-found=true")
		deleteCmd.Env = os.Environ()
		deleteCmd.Run()
	}()
	time.Sleep(1 * time.Second) // Reduced from 3s

	// Update ConfigMap using update manifest
	t.Log("Updating ConfigMap...")
	updateManifestFile := strings.Replace(manifestFile, ".yaml", "-update.yaml", 1)
	updateCmd := exec.Command("kubectl", "apply", "-f", updateManifestFile)
	updateCmd.Env = os.Environ()
	if err := updateCmd.Run(); err != nil {
		t.Logf("ConfigMap update failed (might not exist): %v", err)
	}
	time.Sleep(1 * time.Second) // Reduced from 3s

	// Delete using update manifest (backward compatible - includes all resources)
	t.Log("Deleting manifest...")
	deleteCmd := exec.Command("kubectl", "delete", "-f", updateManifestFile)
	deleteCmd.Env = os.Environ()
	if err := deleteCmd.Run(); err != nil {
		t.Logf("Failed to delete manifest: %v", err)
	}
	time.Sleep(2 * time.Second) // Reduced from 3s
	
	t.Log("✅ PHASE 2 COMPLETE: All manifest work finished!")

	// ========================================
	// PHASE 3: STOPPING MONITORING
	// ========================================
	t.Log("")
	t.Log("🛑 PHASE 3: Stopping Faro binary...")
	
	// Stop Faro
	stopFaro(faroCmd)
	
	t.Log("✅ PHASE 3 COMPLETE: Faro binary stopped!")

	// ========================================
	// PHASE 4: LOADING EVENTS JSON
	// ========================================
	t.Log("")
	t.Log("📊 PHASE 4: Loading and analyzing captured JSON events...")
	
	// Validate events
	events, err := readJSONEvents(logDir)
	if err != nil {
		t.Fatalf("Failed to read events: %v", err)
	}

	// Show log locations
	t.Logf("=== LOG LOCATIONS ===")
	t.Logf("Faro logs: %s/logs/", logDir)
	
	// Find JSON export file
	jsonPattern := filepath.Join(logDir, "logs", "events-*.json")
	jsonFiles, err := filepath.Glob(jsonPattern)
	if err == nil && len(jsonFiles) > 0 {
		t.Logf("JSON export: %s", jsonFiles[len(jsonFiles)-1])
	} else {
		t.Logf("JSON export: Not found")
	}
	
	t.Log("✅ PHASE 4 COMPLETE: JSON events loaded successfully!")

	// ========================================
	// PHASE 5: COMPARING DATA
	// ========================================
	t.Log("")
	t.Log("🔍 PHASE 5: Comparing and validating data...")
	
	assertNoDroppedEvents(t, logDir)
	validateExpectation(t, expected, events)
	
	t.Log("✅ PHASE 5 COMPLETE: Data validation finished!")
	t.Log("")
	t.Log("========================================")
	t.Log("🎯 E2E TEST SUMMARY")
	t.Log("========================================")
	t.Logf("   ✅ Phase 1 - Faro binary started: SUCCESS")
	t.Logf("   ✅ Phase 2 - Manifests processed: SUCCESS")
	t.Logf("   ✅ Phase 3 - Faro binary stopped: SUCCESS")
	t.Logf("   ✅ Phase 4 - JSON events loaded: SUCCESS")
	t.Logf("   ✅ Phase 5 - Data validation: SUCCESS")
	t.Log("========================================")
}


func readJSONEvents(logDir string) ([]FaroJSONEvent, error) {
	// Read from dedicated JSON export file only - no fallbacks
	pattern := filepath.Join(logDir, "logs", "events-*.json")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to search for JSON export files: %v", err)
	}
	
	if len(files) == 0 {
		return nil, fmt.Errorf("no JSON export files found at %s - ensure json_export: true is set in config", pattern)
	}
	
	// Use the most recent JSON export file
	jsonFile := files[len(files)-1]
	content, err := os.ReadFile(jsonFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read JSON export file %s: %v", jsonFile, err)
	}
	
	return parseJSONEvents(string(content))
}

func parseJSONEvents(content string) ([]FaroJSONEvent, error) {
	var events []FaroJSONEvent
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		
		// Parse pure JSON lines from dedicated JSON export file
		var event FaroJSONEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// Skip invalid JSON lines but don't fail the entire parse
			continue
		}
		events = append(events, event)
	}
	return events, nil
}

func TestFaroTest3LabelBased(t *testing.T) {
	feature := features.New("Faro Test 3 - Label-Based ConfigMap").
		Assess("should capture ConfigMap events with label selector", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			configFile := "configs/simple-test-3.yaml"
			manifestFile := "manifests/test3-manifest.yaml"
			updateManifestFile := "manifests/test3-manifest-update.yaml"
			logDir := "logs/test3"

			runE2ETestWithManifest(t, ctx, cfg, configFile, manifestFile, logDir, mustExpect(t, configFile, manifestFile, updateManifestFile))
			return ctx
		}).Feature()

	testenv.Test(t, feature)
}

func TestFaroTest4ResourceLabelBased(t *testing.T) {
	feature := features.New("Faro Test 4 - Resource Label-Based ConfigMap").
		Assess("should capture ConfigMap events with resource label pattern", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			configFile := "configs/simple-test-4.yaml"
			manifestFile := "manifests/test4-manifest.yaml"
			updateManifestFile := "manifests/test4-manifest-update.yaml"
			logDir := "logs/test4"

			runE2ETestWithManifest(t, ctx, cfg, configFile, manifestFile, logDir, mustExpect(t, configFile, manifestFile, updateManifestFile))
			return ctx
		}).Feature()

	testenv.Test(t, feature)
}

func TestFaroTest5NamespaceOnly(t *testing.T) {
	feature := features.New("Faro Test 5 - Namespace Only").
		Assess("should capture namespace events only", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			configFile := "configs/simple-test-5.yaml"
			manifestFile := "manifests/test5-manifest.yaml"
			updateManifestFile := "manifests/test5-manifest-update.yaml"
			logDir := "logs/test5"

			runE2ETestWithManifest(t, ctx, cfg, configFile, manifestFile, logDir, mustExpect(t, configFile, manifestFile, updateManifestFile))
			return ctx
		}).Feature()

	testenv.Test(t, feature)
}

func TestFaroTest6Combined(t *testing.T) {
	feature := features.New("Faro Test 6 - Combined Namespace and ConfigMap").
		Assess("should capture both namespace and ConfigMap events", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			configFile := "configs/simple-test-6.yaml"
			manifestFile := "manifests/test6-manifest.yaml"
			updateManifestFile := "manifests/test6-manifest-update.yaml"
			logDir := "logs/test6"

			runE2ETestWithManifest(t, ctx, cfg, configFile, manifestFile, logDir, mustExpect(t, configFile, manifestFile, updateManifestFile))
			return ctx
		}).Feature()

	testenv.Test(t, feature)
}

func TestFaroTest7DualConfigMap(t *testing.T) {
	feature := features.New("Faro Test 7 - Dual ConfigMap Monitoring").
		Assess("should capture ConfigMap events from both namespace and resource configs", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			configFile := "configs/simple-test-7.yaml"
			manifestFile := "manifests/test7-manifest.yaml"
			updateManifestFile := "manifests/test7-manifest-update.yaml"
			logDir := "logs/test7"

			runE2ETestWithManifest(t, ctx, cfg, configFile, manifestFile, logDir, mustExpect(t, configFile, manifestFile, updateManifestFile))
			return ctx
		}).Feature()

	testenv.Test(t, feature)
}

func TestFaroTest8MultipleNamespaces(t *testing.T) {
	feature := features.New("Faro Test 8 - Multiple Namespaces with Label Selector").
		Assess("should capture namespace events with label selector", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			configFile := "configs/simple-test-8.yaml"
			manifestFile := "manifests/test8-manifest.yaml"
			updateManifestFile := "manifests/test8-manifest-update.yaml"
			logDir := "logs/test8"

			runE2ETestWithManifest(t, ctx, cfg, configFile, manifestFile, logDir, mustExpect(t, configFile, manifestFile, updateManifestFile))
			return ctx
		}).Feature()

	testenv.Test(t, feature)
}

func TestFaroTest9MultiNamespaceControllerBug(t *testing.T) {
	feature := features.New("Faro Test 9 - Multi-Namespace Controller Configuration Bug").
		Assess("should monitor resources in ALL configured namespaces, not just the first one", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			configFile := "configs/simple-test-9.yaml"
			manifestFile := "manifests/test9-manifest.yaml"
			updateManifestFile := "manifests/test9-manifest-update.yaml"
			logDir := "logs/test9"

			runE2ETestWithManifest(t, ctx, cfg, configFile, manifestFile, logDir, mustExpect(t, configFile, manifestFile, updateManifestFile))
			return ctx
		}).Feature()

	testenv.Test(t, feature)
}

func assertEventIsOrderable(t *testing.T, act FaroJSONEvent, resourcePath string) {
	t.Helper()
	if act.EventTime == "" {
		t.Errorf("✗ NO eventTime: %s %s %s - this event cannot be placed in a timeline",
			act.EventType, act.GVR, resourcePath)
		return
	}
	if _, err := time.Parse(time.RFC3339Nano, act.EventTime); err != nil {
		t.Errorf("✗ BAD eventTime: %s %s %s has %q, not RFC3339Nano: %v",
			act.EventType, act.GVR, resourcePath, act.EventTime, err)
	}
	if strings.HasPrefix(act.Timestamp, "0001-01-01") {
		t.Errorf("✗ ZERO timestamp: %s %s %s carries %q - omit it rather than emit the zero value",
			act.EventType, act.GVR, resourcePath, act.Timestamp)
	}
}

func validateEvents(t *testing.T, expected []FaroJSONEvent, actual []FaroJSONEvent) {
	t.Logf("=== ACTUAL EVENTS FOUND (%d) ===", len(actual))
	for i, act := range actual {
		var resourcePath string
		if act.Namespace == "" {
			// Cluster-scoped resource - just show the name
			resourcePath = "/" + act.Name
		} else {
			// Namespaced resource - show namespace/name
			resourcePath = "/" + act.Namespace + "/" + act.Name
		}
		t.Logf("  %d. %s %s %s", i+1, act.EventType, act.GVR, resourcePath)
	}
	
	t.Logf("=== VALIDATION RESULTS ===")
	for _, exp := range expected {
		found := false
		for _, act := range actual {
			if exp.EventType == act.EventType && exp.GVR == act.GVR && 
			   exp.Namespace == act.Namespace && exp.Name == act.Name {
				found = true
				var resourcePath string
				if exp.Namespace == "" {
					// Cluster-scoped resource - just show the name
					resourcePath = "/" + exp.Name
				} else {
					// Namespaced resource - show namespace/name
					resourcePath = "/" + exp.Namespace + "/" + exp.Name
				}
				t.Logf("✓ MATCHED: %s %s %s", exp.EventType, exp.GVR, resourcePath)
				assertEventIsOrderable(t, act, resourcePath)
				break
			}
		}
		if !found {
			var resourcePath string
			if exp.Namespace == "" {
				// Cluster-scoped resource - just show the name
				resourcePath = "/" + exp.Name
			} else {
				// Namespaced resource - show namespace/name
				resourcePath = "/" + exp.Namespace + "/" + exp.Name
			}
			t.Errorf("✗ MISSING: %s %s %s", exp.EventType, exp.GVR, resourcePath)
		}
	}
}




// TestFaroTest10ConfigurableFields proves the two halves of configurable event
// fields against a real cluster: a configured path reaches the emitted event,
// and a Secret's data does not - even though the config asks for it.
//
// The denylist is the reason this is an e2e test and not only a unit test. A
// Secret's data is base64, not encryption, and the capture file it would land
// in sits on shared storage in a real deployment. It is worth proving against
// a live apiserver that nothing in the informer path reintroduces it.
func TestFaroTest10ConfigurableFields(t *testing.T) {
	t.Parallel()

	const cfg, create, update = "configs/simple-test-10.yaml", "manifests/test10-manifest.yaml", "manifests/test10-manifest-update.yaml"
	expected := handWritten(t, []FaroJSONEvent{
		{EventType: "ADDED", GVR: "v1/configmaps", Namespace: "faro-test-10", Name: "test-config-10"},
		{EventType: "UPDATED", GVR: "v1/configmaps", Namespace: "faro-test-10", Name: "test-config-10"},
		{EventType: "DELETED", GVR: "v1/configmaps", Namespace: "faro-test-10", Name: "test-config-10"},
		{EventType: "ADDED", GVR: "v1/secrets", Namespace: "faro-test-10", Name: "test-secret-10"},
		{EventType: "DELETED", GVR: "v1/secrets", Namespace: "faro-test-10", Name: "test-secret-10"},
	}, cfg, create, update)

	runParallelE2ETest(t, "test10", cfg, create, update, expected)

	events, err := readJSONEvents("logs/test10")
	if err != nil {
		t.Fatalf("reading emitted events: %v", err)
	}

	// Assert the EXACT field set per event, not merely that something is
	// present. A weaker check passes while extraction is stale (the UPDATE
	// still reporting the created value) or over-broad (emitting paths nobody
	// asked for), and both are the failures worth catching.
	// Matched on NAME as well as GVR and type. The namespace also carries
	// kube-root-ca.crt, which is a v1/configmaps ADDED event with no fields -
	// matching on GVR alone would pick whichever came first and assert against
	// the wrong object.
	fieldsOf := func(evs []FaroJSONEvent, gvr, eventType, name string) (map[string]interface{}, bool) {
		for _, e := range evs {
			if e.GVR == gvr && e.EventType == eventType && e.Name == name {
				return e.Fields, true
			}
		}
		return nil, false
	}
	// Fields are emitted NESTED, so the config's "data.key1" arrives as
	// {"data":{"key1":...}}. Walk the path rather than indexing a dotted key -
	// the whole point of nesting is that no emitted key contains a dot.
	at := func(m map[string]interface{}, path string) (interface{}, bool) {
		var cur interface{} = m
		for _, seg := range strings.Split(path, ".") {
			mm, ok := cur.(map[string]interface{})
			if !ok {
				return nil, false
			}
			cur, ok = mm[seg]
			if !ok {
				return nil, false
			}
		}
		return cur, true
	}
	// Counting scalar LEAVES keeps "exactly these fields and no others" an
	// exact claim now that the shape is a tree rather than a flat map.
	var leafCount func(interface{}) int
	leafCount = func(v interface{}) int {
		m, ok := v.(map[string]interface{})
		if !ok {
			return 1
		}
		n := 0
		for _, vv := range m {
			n += leafCount(vv)
		}
		return n
	}
	same := func(got map[string]interface{}, want map[string]string) bool {
		if leafCount(got) != len(want) {
			return false
		}
		for k, v := range want {
			g, ok := at(got, k)
			if !ok {
				return false
			}
			if s, _ := g.(string); s != v {
				return false
			}
		}
		return true
	}

	// ADDED carries the created value.
	add, ok := fieldsOf(events, "v1/configmaps", "ADDED", "test-config-10")
	if !ok {
		t.Fatal("no ADDED configmap event")
	}
	if !same(add, map[string]string{"data.key1": "value1"}) {
		t.Errorf("ADDED configmap fields = %#v, want exactly {data.key1: value1}", add)
	}

	// UPDATED carries the NEW value. This is the assertion that fails if the
	// object is read once and cached: accepting "either value" would pass
	// while every update reported what the object looked like at creation.
	upd, ok := fieldsOf(events, "v1/configmaps", "UPDATED", "test-config-10")
	if !ok {
		t.Fatal("no UPDATED configmap event")
	}
	if !same(upd, map[string]string{"data.key1": "value1-updated"}) {
		t.Errorf("UPDATED configmap fields = %#v, want exactly {data.key1: value1-updated}", upd)
	}

	// The Secret asked for type, data and data.password. EXACTLY type comes
	// back: the denylist is surgical, not a blanket refusal that would also
	// drop the harmless field - and nothing else leaks in either.
	sec, ok := fieldsOf(events, "v1/secrets", "ADDED", "test-secret-10")
	if !ok {
		t.Fatal("no ADDED secret event - the denylist assertion never ran")
	}
	if !same(sec, map[string]string{"type": "Opaque"}) {
		t.Fatalf("SECRET: fields = %#v, want exactly {type: Opaque}", sec)
	}

	// DELETED carries the object's last state - the updated value, not the
	// created one and not nothing - so a consumer can tell what went away.
	del, ok := fieldsOf(events, "v1/configmaps", "DELETED", "test-config-10")
	if !ok {
		t.Fatal("no DELETED configmap event")
	}
	if !same(del, map[string]string{"data.key1": "value1-updated"}) {
		t.Errorf("DELETED configmap fields = %#v, want exactly {data.key1: value1-updated}", del)
	}

	// The Secret's last state goes through the same denylist on delete: a
	// DELETED event that carries state must not be where data.password leaks.
	secDel, ok := fieldsOf(events, "v1/secrets", "DELETED", "test-secret-10")
	if !ok {
		t.Fatal("no DELETED secret event - the denylist-on-delete assertion never ran")
	}
	if !same(secDel, map[string]string{"type": "Opaque"}) {
		t.Fatalf("SECRET DELETED: fields = %#v, want exactly {type: Opaque}", secDel)
	}
}
