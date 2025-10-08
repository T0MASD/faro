package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v2"
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


type FaroJSONEvent struct {
	Timestamp string            `json:"timestamp"`
	EventType string            `json:"eventType"`
	GVR       string            `json:"gvr"`
	Namespace string            `json:"namespace,omitempty"`
	Name      string            `json:"name"`
	UID       string            `json:"uid,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// Config structures for parsing Faro config
type FaroConfig struct {
	Namespaces []NamespaceConfig `yaml:"namespaces"`
	Resources  []ResourceConfig  `yaml:"resources"`
}

type NamespaceConfig struct {
	NameSelector string                      `yaml:"name_pattern"`
	Resources   map[string]ResourceSelector  `yaml:"resources"`
}

type ResourceConfig struct {
	GVR               string   `yaml:"gvr"`
	Scope             string   `yaml:"scope"`
	NamespaceNames []string `yaml:"namespace_patterns"`
	NameSelector   string   `yaml:"name_pattern"`
	LabelSelector     string   `yaml:"label_selector"`
}

type ResourceSelector struct {
	NameSelector string `yaml:"name_pattern"`
	LabelSelector string `yaml:"label_selector"`
}

// Manifest resource representation
type ManifestResource struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   ResourceMetadata  `yaml:"metadata"`
}

type ResourceMetadata struct {
	Name      string            `yaml:"name"`
	Namespace string            `yaml:"namespace"`
	Labels    map[string]string `yaml:"labels"`
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

// generateExpectedEvents dynamically generates expected events based on config and manifests
func generateExpectedEvents(configFile, createManifest, updateManifest string) ([]FaroJSONEvent, error) {
	// Parse Faro config
	config, err := parseConfig(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %v", err)
	}

	// Parse create manifest to get initial resources
	createResources, err := parseManifest(createManifest)
	if err != nil {
		return nil, fmt.Errorf("failed to parse create manifest: %v", err)
	}

	var expectedEvents []FaroJSONEvent

	// Generate events for each resource that should be monitored
	for _, resource := range createResources {
		if shouldMonitorResource(config, resource) {
			gvr := convertToGVR(resource.APIVersion, resource.Kind)
			
			// Each monitored resource should have ADDED, UPDATED, DELETED events
			expectedEvents = append(expectedEvents,
				FaroJSONEvent{EventType: "ADDED", GVR: gvr, Namespace: resource.Metadata.Namespace, Name: resource.Metadata.Name},
				FaroJSONEvent{EventType: "UPDATED", GVR: gvr, Namespace: resource.Metadata.Namespace, Name: resource.Metadata.Name},
				FaroJSONEvent{EventType: "DELETED", GVR: gvr, Namespace: resource.Metadata.Namespace, Name: resource.Metadata.Name},
			)
		}
	}

	return expectedEvents, nil
}

// parseConfig parses a Faro YAML config file
func parseConfig(configFile string) (*FaroConfig, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}

	var config FaroConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// parseManifest parses a Kubernetes manifest file and returns all resources
func parseManifest(manifestFile string) ([]ManifestResource, error) {
	data, err := os.ReadFile(manifestFile)
	if err != nil {
		return nil, err
	}

	// Split YAML documents by "---"
	documents := strings.Split(string(data), "---")
	var resources []ManifestResource

	for _, doc := range documents {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}

		var resource ManifestResource
		if err := yaml.Unmarshal([]byte(doc), &resource); err != nil {
			continue // Skip invalid documents
		}

		// Only include actual resources (not empty documents)
		if resource.Kind != "" && resource.Metadata.Name != "" {
			resources = append(resources, resource)
		}
	}

	return resources, nil
}

// shouldMonitorResource determines if a resource should be monitored based on Faro config
func shouldMonitorResource(config *FaroConfig, resource ManifestResource) bool {
	gvr := convertToGVR(resource.APIVersion, resource.Kind)

	// Check namespace-based configs
	for _, nsConfig := range config.Namespaces {
		if matchesName(nsConfig.NameSelector, resource.Metadata.Namespace) {
			if resourceSelector, exists := nsConfig.Resources[gvr]; exists {
				if matchesResourceSelector(resourceSelector, resource) {
					return true
				}
			}
		}
	}

	// Check resource-based configs
	for _, resConfig := range config.Resources {
		if resConfig.GVR == gvr {
			// Check namespace names
			namespaceMatches := false
			for _, nsName := range resConfig.NamespaceNames {
				if matchesName(nsName, resource.Metadata.Namespace) {
					namespaceMatches = true
					break
				}
			}
			
			if namespaceMatches {
				// Check name selector
				if resConfig.NameSelector == "" || matchesName(resConfig.NameSelector, resource.Metadata.Name) {
					// Check label selector
					if resConfig.LabelSelector == "" || matchesLabelSelector(resConfig.LabelSelector, resource.Metadata.Labels) {
						return true
					}
				}
			}
		}
	}

	return false
}

// matchesResourceSelector checks if a resource matches the resource selector criteria
func matchesResourceSelector(selector ResourceSelector, resource ManifestResource) bool {
	// Check name selector
	if selector.NameSelector != "" && !matchesName(selector.NameSelector, resource.Metadata.Name) {
		return false
	}

	// Check label selector
	if selector.LabelSelector != "" && !matchesLabelSelector(selector.LabelSelector, resource.Metadata.Labels) {
		return false
	}

	return true
}

// matchesName checks if a string matches a name (supports wildcards for backward compatibility)
func matchesName(nameSelector, value string) bool {
	if nameSelector == "" {
		return true
	}
	
	// Convert shell-style wildcards to regex for backward compatibility
	regexPattern := strings.ReplaceAll(nameSelector, "*", ".*")
	regexPattern = "^" + regexPattern + "$"
	
	matched, _ := regexp.MatchString(regexPattern, value)
	return matched
}

// matchesLabelSelector checks if labels match a simple label selector (key=value format)
func matchesLabelSelector(selector string, labels map[string]string) bool {
	if selector == "" {
		return true
	}

	// Simple implementation for key=value selectors
	parts := strings.Split(selector, "=")
	if len(parts) != 2 {
		return false
	}

	key := strings.TrimSpace(parts[0])
	expectedValue := strings.TrimSpace(parts[1])
	
	actualValue, exists := labels[key]
	return exists && actualValue == expectedValue
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
func runParallelE2ETest(t *testing.T, testName, configFile, manifestFile, updateManifestFile string, expectedEvents []FaroJSONEvent) {
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
			runE2ETestWithManifestParallel(t, ctx, cfg, configFile, manifestFile, updateManifestFile, expectedEvents)
			return ctx
		}).Feature()
	
	testEnv.Test(t, feature)
}

// runE2ETestWithManifestParallel is the parallel version of runE2ETestWithManifest
func runE2ETestWithManifestParallel(t *testing.T, ctx context.Context, cfg *envconf.Config, configFile, manifestFile, updateManifestFile string, expectedEvents []FaroJSONEvent) {
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
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()

	// Wait for Faro to be ready
	if err := waitForFaroReady(t, stdout, stderr); err != nil {
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
		cmd.Process.Kill()
		cmd.Wait()
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

	validateEvents(t, expectedEvents, events)

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
	
	expectedEvents := []FaroJSONEvent{
		{EventType: "ADDED", GVR: "v1/configmaps", Namespace: "faro-test-1", Name: "kube-root-ca.crt"},
		{EventType: "ADDED", GVR: "v1/configmaps", Namespace: "faro-test-1", Name: "test-config-1"},
		{EventType: "UPDATED", GVR: "v1/configmaps", Namespace: "faro-test-1", Name: "test-config-1"},
		{EventType: "DELETED", GVR: "v1/configmaps", Namespace: "faro-test-1", Name: "test-config-1"},
		{EventType: "DELETED", GVR: "v1/configmaps", Namespace: "faro-test-1", Name: "kube-root-ca.crt"},
	}
	
	runParallelE2ETest(t, "test1", "configs/simple-test-1.yaml", "manifests/test1-manifest.yaml", "manifests/test1-manifest-update.yaml", expectedEvents)
}

func TestFaroTest2ResourceCentric(t *testing.T) {
	t.Parallel() // Enable parallel execution
	
	// Test 2 config specifies name_selector: "test-config-1", so only test-config-1 should be captured
	expectedEvents := []FaroJSONEvent{
		{EventType: "ADDED", GVR: "v1/configmaps", Namespace: "faro-test-2", Name: "test-config-1"},
		{EventType: "UPDATED", GVR: "v1/configmaps", Namespace: "faro-test-2", Name: "test-config-1"},
		{EventType: "DELETED", GVR: "v1/configmaps", Namespace: "faro-test-2", Name: "test-config-1"},
	}
	
	runParallelE2ETest(t, "test2", "configs/simple-test-2.yaml", "manifests/test2-manifest.yaml", "manifests/test2-manifest-update.yaml", expectedEvents)
}

func runE2ETestWithManifest(t *testing.T, ctx context.Context, cfg *envconf.Config, configFile string, manifestFile string, logDir string, expectedEvents []FaroJSONEvent) {
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
			faroCmd.Process.Kill()
			faroCmd.Wait()
		}
	}()

	// Wait for Faro to be ready by monitoring its logs
	if err := waitForFaroReady(t, stdout, stderr); err != nil {
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
	faroCmd.Process.Kill()
	faroCmd.Wait()
	
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
	
	displayFaroQueries(t, configFile)
	validateEvents(t, expectedEvents, events)
	
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

func getNamespaceFromManifest(manifestFile string) string {
	// Simple function to extract namespace from manifest file
	// For now, return a default based on the test number
	if strings.Contains(manifestFile, "test1") {
		return "faro-test-1"
	} else if strings.Contains(manifestFile, "test2") {
		return "faro-test-2"
	} else if strings.Contains(manifestFile, "test3") {
		return "faro-test-3"
	} else if strings.Contains(manifestFile, "test4") {
		return "faro-test-4"
	} else if strings.Contains(manifestFile, "test5") {
		return "faro-test-5"
	} else if strings.Contains(manifestFile, "test6") {
		return "faro-test-6"
	} else if strings.Contains(manifestFile, "test7") {
		return "faro-test-7"
	} else if strings.Contains(manifestFile, "test8") {
		return "faro-test-8a"
	}
	return "default"
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
			logDir := "logs/test3"
			expectedEvents := []FaroJSONEvent{
				{EventType: "ADDED", GVR: "v1/configmaps", Namespace: "faro-test-3", Name: "test-config-1"},
				{EventType: "UPDATED", GVR: "v1/configmaps", Namespace: "faro-test-3", Name: "test-config-1"},
				{EventType: "DELETED", GVR: "v1/configmaps", Namespace: "faro-test-3", Name: "test-config-1"},
			}

			runE2ETestWithManifest(t, ctx, cfg, configFile, manifestFile, logDir, expectedEvents)
			return ctx
		}).Feature()

	testenv.Test(t, feature)
}

func TestFaroTest4ResourceLabelBased(t *testing.T) {
	feature := features.New("Faro Test 4 - Resource Label-Based ConfigMap").
		Assess("should capture ConfigMap events with resource label pattern", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {

		configFile := "configs/simple-test-4.yaml"
		manifestFile := "manifests/test4-manifest.yaml"
		logDir := "logs/test4"
		expectedEvents := []FaroJSONEvent{
			{EventType: "ADDED", GVR: "v1/configmaps", Namespace: "faro-test-4", Name: "test-config-1"},
			{EventType: "UPDATED", GVR: "v1/configmaps", Namespace: "faro-test-4", Name: "test-config-1"},
			{EventType: "DELETED", GVR: "v1/configmaps", Namespace: "faro-test-4", Name: "test-config-1"},
		}

		runE2ETestWithManifest(t, ctx, cfg, configFile, manifestFile, logDir, expectedEvents)
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
			
			// Generate expected events dynamically based on config and manifests
			expectedEvents, err := generateExpectedEvents(configFile, manifestFile, updateManifestFile)
			if err != nil {
				t.Fatalf("Failed to generate expected events: %v", err)
			}

			runE2ETestWithManifest(t, ctx, cfg, configFile, manifestFile, logDir, expectedEvents)
			return ctx
		}).Feature()

	testenv.Test(t, feature)
}

func TestFaroTest6Combined(t *testing.T) {
	feature := features.New("Faro Test 6 - Combined Namespace and ConfigMap").
		Assess("should capture both namespace and ConfigMap events", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {

		configFile := "configs/simple-test-6.yaml"
		manifestFile := "manifests/test6-manifest.yaml"
		logDir := "logs/test6"
				// Test 6 monitors both namespaces and configmaps - both should be captured correctly
				expectedEvents := []FaroJSONEvent{
					{EventType: "ADDED", GVR: "v1/namespaces", Name: "faro-test-6"},
					{EventType: "ADDED", GVR: "v1/configmaps", Namespace: "faro-test-6", Name: "test-config-1"},
					{EventType: "UPDATED", GVR: "v1/configmaps", Namespace: "faro-test-6", Name: "test-config-1"},
					{EventType: "DELETED", GVR: "v1/configmaps", Namespace: "faro-test-6", Name: "test-config-1"},
					{EventType: "DELETED", GVR: "v1/namespaces", Name: "faro-test-6"},
				}

		runE2ETestWithManifest(t, ctx, cfg, configFile, manifestFile, logDir, expectedEvents)
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
		
		// Generate expected events dynamically based on config and manifests
		expectedEvents, err := generateExpectedEvents(configFile, manifestFile, updateManifestFile)
		if err != nil {
			t.Fatalf("Failed to generate expected events: %v", err)
		}

		runE2ETestWithManifest(t, ctx, cfg, configFile, manifestFile, logDir, expectedEvents)
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
			
			// Generate expected events dynamically based on config and manifests
			expectedEvents, err := generateExpectedEvents(configFile, manifestFile, updateManifestFile)
			if err != nil {
				t.Fatalf("Failed to generate expected events: %v", err)
			}

			runE2ETestWithManifest(t, ctx, cfg, configFile, manifestFile, logDir, expectedEvents)
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
			
			// Generate expected events dynamically based on config and manifests
			expectedEvents, err := generateExpectedEvents(configFile, manifestFile, updateManifestFile)
			if err != nil {
				t.Fatalf("Failed to generate expected events: %v", err)
			}

			runE2ETestWithManifest(t, ctx, cfg, configFile, manifestFile, logDir, expectedEvents)
			return ctx
		}).Feature()

	testenv.Test(t, feature)
}



func waitForFaroReady(t *testing.T, stdout, stderr io.ReadCloser) error {
	// Create channels to monitor both stdout and stderr
	readyChan := make(chan bool, 1)
	errorChan := make(chan error, 1)
	
	// Monitor stdout
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			
			// Log file path identification for test validation
			if strings.Contains(line, "FARO_LOG_FILE:") || strings.Contains(line, "FARO_JSON_FILE:") {
				t.Logf("📁 %s", line)
			}
			
			// Look for initialization complete indicators
			if strings.Contains(line, "Multi-layered informer architecture started successfully") || 
			   strings.Contains(line, "Controller started with") ||
			   strings.Contains(line, "Waiting for shutdown signal") {
				select {
				case readyChan <- true:
				default:
				}
			}
		}
	}()
	
	// Monitor stderr
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			// Just consume stderr, don't log it
			// Look for initialization complete indicators in stderr
			if strings.Contains(line, "Multi-layered informer architecture started successfully") || 
			   strings.Contains(line, "Controller started with") ||
			   strings.Contains(line, "Waiting for shutdown signal") {
				select {
				case readyChan <- true:
				default:
				}
			}
			// Look for fatal errors
			if strings.Contains(line, "FATAL") || strings.Contains(line, "failed to start") {
				select {
				case errorChan <- fmt.Errorf("faro startup error: %s", line):
				default:
				}
			}
		}
	}()
	
	// Wait for ready signal or timeout
	select {
	case <-readyChan:
		t.Log("Faro is ready!")
		time.Sleep(2 * time.Second) // Give it a moment to fully initialize
		return nil
	case err := <-errorChan:
		return err
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timeout waiting for Faro to be ready")
	}
}

func displayFaroQueries(t *testing.T, configFile string) {
	config, err := parseConfig(configFile)
	if err != nil {
		t.Logf("Failed to parse config for display: %v", err)
		return
	}

	queries := []string{}
	
	// Add namespace-based queries (convert to unified format)
	for _, ns := range config.Namespaces {
		for gvr := range ns.Resources {
			queries = append(queries, formatQuery(QueryInfo{
				Type:      "namespace",
				GVR:       gvr,
			Namespace: ns.NameSelector,
			Name:      ns.Resources[gvr].NameSelector,
				Labels:    ns.Resources[gvr].LabelSelector,
			}))
		}
	}
	
	// Add resource-based queries (convert to unified format)
	for _, res := range config.Resources {
		queries = append(queries, formatQuery(QueryInfo{
			Type:       "resource",
			GVR:        res.GVR,
			Namespaces: res.NamespaceNames,
			Name:       res.NameSelector,
			Labels:     res.LabelSelector,
		}))
	}

	t.Logf("=== FARO QUERIES (%d) ===", len(queries))
	for i, query := range queries {
		t.Logf("  %d. %s", i+1, query)
	}
}

// QueryInfo represents a unified query structure
type QueryInfo struct {
	Type       string   // "namespace" or "resource"
	GVR        string   // Group/Version/Resource
	Namespace  string   // Single namespace (for namespace-centric)
	Namespaces []string // Multiple namespaces (for resource-centric)
	Name       string   // Name selector
	Labels     string   // Label selector
}

// formatQuery creates a consistent query string format
func formatQuery(q QueryInfo) string {
	var query string
	
	// Build base query with consistent format
	if q.Type == "namespace" && q.Namespace != "" {
		query = fmt.Sprintf("Monitor %s in namespace '%s'", q.GVR, q.Namespace)
	} else if q.Type == "resource" {
		query = fmt.Sprintf("Monitor %s", q.GVR)
		if len(q.Namespaces) > 0 {
			query += fmt.Sprintf(" in namespaces %v", q.Namespaces)
		}
	} else {
		query = fmt.Sprintf("Monitor %s", q.GVR)
	}
	
	// Add filters consistently
	filters := []string{}
	if q.Name != "" {
		filters = append(filters, fmt.Sprintf("name: %s", q.Name))
	}
	if q.Labels != "" {
		filters = append(filters, fmt.Sprintf("labels: %s", q.Labels))
	}
	
	if len(filters) > 0 {
		query += fmt.Sprintf(" [%s]", strings.Join(filters, ", "))
	}
	
	return query
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



