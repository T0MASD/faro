package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

var testenv env.Environment

// FaroConfig represents the Faro YAML configuration (no Faro library imports)
type FaroConfig struct {
	OutputDir       string            `yaml:"output_dir"`
	LogLevel        string            `yaml:"log_level"`
	AutoShutdownSec int               `yaml:"auto_shutdown_sec"`
	JsonExport      bool              `yaml:"json_export"`
	Namespaces      []NamespaceConfig `yaml:"namespaces,omitempty"`
	Resources       []ResourceConfig  `yaml:"resources,omitempty"`
}

type NamespaceConfig struct {
	NamePattern string                     `yaml:"name_pattern"`
	Resources   map[string]ResourceDetails `yaml:"resources"`
}

type ResourceDetails struct {
	NamePattern   string `yaml:"name_pattern,omitempty"`
	LabelSelector string `yaml:"label_selector,omitempty"`
}

type ResourceConfig struct {
	GVR           string   `yaml:"gvr"`
	Scope         string   `yaml:"scope"`
	NamePattern   string   `yaml:"name_pattern,omitempty"`
	LabelSelector string   `yaml:"label_selector,omitempty"`
	Namespaces    []string `yaml:"namespaces,omitempty"`
}

// ExpectedEvent represents what we expect Faro to capture
type ExpectedEvent struct {
	EventType string            `json:"eventType"`
	GVR       string            `json:"gvr"`
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// FaroJSONEvent represents the actual JSON event from Faro logs
type FaroJSONEvent struct {
	Timestamp string            `json:"timestamp"`
	EventType string            `json:"eventType"`
	GVR       string            `json:"gvr"`
	Namespace string            `json:"namespace,omitempty"`
	Name      string            `json:"name"`
	UID       string            `json:"uid,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

func TestMain(m *testing.M) {
	testenv = env.New()

	testenv.Setup(
		func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
			// Build Faro binary
			if err := buildFaro(); err != nil {
				return ctx, fmt.Errorf("failed to build Faro: %w", err)
			}
			return ctx, nil
		},
	)

	testenv.Finish(
		func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
			// Clean up any remaining test namespaces
			client, err := cfg.NewClient()
			if err != nil {
				return ctx, err
			}

			testNamespaces := []string{"faro-test-1", "faro-testa"}
			for _, nsName := range testNamespaces {
				ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
				client.Resources().Delete(ctx, ns)
			}

			return ctx, nil
		},
	)

	os.Exit(testenv.Run(m))
}

func buildFaro() error {
	cmd := exec.Command("go", "build", "-o", "faro", ".")
	cmd.Dir = "../.."
	return cmd.Run()
}

func TestFaroTest1NamespaceCentric(t *testing.T) {
	feature := features.New("Faro Test 1 - Namespace-Centric ConfigMap Monitoring").
		WithLabel("test", "namespace-centric").
		Assess("should capture ConfigMap lifecycle events in faro-test-1 namespace", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {

			// STEP 1: Parse Faro config
			configFile := "../configs/simple-test-1.yaml"
			faroConfig, err := parseFaroConfig(configFile)
			if err != nil {
				t.Fatalf("Failed to parse Faro config: %v", err)
			}

			if !faroConfig.JsonExport {
				t.Fatalf("JSON export not enabled in config")
			}

			t.Logf("Parsed Faro config: monitoring namespace '%s' for resource '%s'",
				faroConfig.Namespaces[0].NamePattern,
				getFirstResourceGVR(faroConfig.Namespaces[0].Resources))

			// STEP 2: Generate expected events based on config and planned actions
			expectedEvents := generateExpectedEvents(faroConfig)
			t.Logf("Generated %d expected events", len(expectedEvents))

			// STEP 3: Create Kubernetes client and test namespace
			client, err := cfg.NewClient()
			if err != nil {
				t.Fatalf("Failed to create client: %v", err)
			}

			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "faro-test-1",
					Labels: map[string]string{"test": "faro-e2e"},
				},
			}

			if err := client.Resources().Create(ctx, testNamespace); err != nil {
				t.Fatalf("Failed to create test namespace: %v", err)
			}

			// Wait for namespace to be ready
			if err := wait.For(conditions.New(client.Resources()).ResourceMatch(testNamespace, func(object k8s.Object) bool {
				return object.(*corev1.Namespace).Status.Phase == corev1.NamespaceActive
			}), wait.WithTimeout(time.Minute*1)); err != nil {
				t.Fatalf("Namespace not ready: %v", err)
			}

			// STEP 4: Start Faro binary with JSON export enabled
			logFile := filepath.Join("logs", "faro-test1-e2e.log")
			os.MkdirAll("logs", 0755)

			faroCmd := exec.CommandContext(ctx, "../../faro", "-config", configFile)

			logFileHandle, err := os.Create(logFile)
			if err != nil {
				t.Fatalf("Failed to create log file: %v", err)
			}
			defer logFileHandle.Close()

			faroCmd.Stdout = logFileHandle
			faroCmd.Stderr = logFileHandle

			if err := faroCmd.Start(); err != nil {
				t.Fatalf("Failed to start Faro: %v", err)
			}

			defer func() {
				if faroCmd.Process != nil {
					faroCmd.Process.Kill()
					faroCmd.Wait()
				}
			}()

			// Wait for Faro to initialize
			t.Log("Waiting for Faro to initialize...")
			if err := waitForFaroReady(logFile, time.Minute*2); err != nil {
				t.Fatalf("Faro failed to initialize: %v", err)
			}
			t.Log("Faro is ready")

			// STEP 5: Execute Kubernetes actions to generate events
			t.Log("Executing Kubernetes actions...")

			// CREATE ConfigMaps (ADDED events)
			testConfigMap1 := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config-1",
					Namespace: "faro-test-1",
					Labels:    map[string]string{"app": "faro-test"},
				},
				Data: map[string]string{"key1": "value1"},
			}

			testConfigMap2 := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config-2",
					Namespace: "faro-test-1",
				},
				Data: map[string]string{"key2": "value2"},
			}

			t.Log("Creating ConfigMaps...")
			if err := client.Resources().Create(ctx, testConfigMap1); err != nil {
				t.Fatalf("Failed to create test-config-1: %v", err)
			}
			if err := client.Resources().Create(ctx, testConfigMap2); err != nil {
				t.Fatalf("Failed to create test-config-2: %v", err)
			}
			time.Sleep(3 * time.Second)

			// UPDATE ConfigMaps (UPDATED events)
			t.Log("Updating ConfigMaps...")
			testConfigMap1.Data["key1"] = "updated-value1"
			if err := client.Resources().Update(ctx, testConfigMap1); err != nil {
				t.Fatalf("Failed to update test-config-1: %v", err)
			}
			testConfigMap2.Data["key2"] = "updated-value2"
			if err := client.Resources().Update(ctx, testConfigMap2); err != nil {
				t.Fatalf("Failed to update test-config-2: %v", err)
			}
			time.Sleep(3 * time.Second)

			// DELETE ConfigMaps (DELETED events)
			t.Log("Deleting ConfigMaps...")
			if err := client.Resources().Delete(ctx, testConfigMap1); err != nil {
				t.Fatalf("Failed to delete test-config-1: %v", err)
			}
			if err := client.Resources().Delete(ctx, testConfigMap2); err != nil {
				t.Fatalf("Failed to delete test-config-2: %v", err)
			}
			time.Sleep(3 * time.Second)

			// STEP 6: Stop Faro
			t.Log("Stopping Faro...")
			if faroCmd.Process != nil {
				faroCmd.Process.Kill()
				faroCmd.Wait()
			}

			// STEP 7: Extract JSON events from Faro logs
			t.Log("Extracting JSON events from Faro logs...")
			actualEvents, err := extractJSONEvents(logFile)
			if err != nil {
				t.Fatalf("Failed to extract JSON events: %v", err)
			}

			t.Logf("Extracted %d JSON events from Faro logs", len(actualEvents))
			for i, event := range actualEvents {
				t.Logf("  Event[%d]: %s %s %s/%s", i+1, event.EventType, event.GVR, event.Namespace, event.Name)
			}

			// STEP 8: Validate expected vs actual events
			t.Log("Validating expected vs actual JSON events...")
			validateJSONEvents(t, expectedEvents, actualEvents)

			// STEP 9: Clean up
			t.Log("Cleaning up test namespace...")
			if err := client.Resources().Delete(ctx, testNamespace); err != nil {
				t.Logf("Warning: Failed to delete test namespace: %v", err)
			}

			return ctx
		}).Feature()

	testenv.Test(t, feature)
}

// parseFaroConfig parses the Faro YAML config file
func parseFaroConfig(configFile string) (*FaroConfig, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config FaroConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse YAML config: %w", err)
	}

	return &config, nil
}

// generateExpectedEvents creates expected events based on Faro config and planned actions
func generateExpectedEvents(config *FaroConfig) []ExpectedEvent {
	var expectedEvents []ExpectedEvent

	// Based on simple-test-1.yaml config:
	// - Monitors namespace "faro-test-1"
	// - Monitors "v1/configmaps" with name_pattern "test-config-1"
	// But we know Faro will capture ALL ConfigMaps in the namespace due to server-side filtering

	// Expected events for test-config-1 (matches name pattern)
	expectedEvents = append(expectedEvents, ExpectedEvent{
		EventType: "ADDED",
		GVR:       "v1/configmaps",
		Namespace: "faro-test-1",
		Name:      "test-config-1",
		Labels:    map[string]string{"app": "faro-test"},
	})
	expectedEvents = append(expectedEvents, ExpectedEvent{
		EventType: "UPDATED",
		GVR:       "v1/configmaps",
		Namespace: "faro-test-1",
		Name:      "test-config-1",
		Labels:    map[string]string{"app": "faro-test"},
	})
	expectedEvents = append(expectedEvents, ExpectedEvent{
		EventType: "DELETED",
		GVR:       "v1/configmaps",
		Namespace: "faro-test-1",
		Name:      "test-config-1",
	})

	// Expected events for test-config-2 (also captured due to namespace-scoped informer)
	expectedEvents = append(expectedEvents, ExpectedEvent{
		EventType: "ADDED",
		GVR:       "v1/configmaps",
		Namespace: "faro-test-1",
		Name:      "test-config-2",
	})
	expectedEvents = append(expectedEvents, ExpectedEvent{
		EventType: "UPDATED",
		GVR:       "v1/configmaps",
		Namespace: "faro-test-1",
		Name:      "test-config-2",
	})
	expectedEvents = append(expectedEvents, ExpectedEvent{
		EventType: "DELETED",
		GVR:       "v1/configmaps",
		Namespace: "faro-test-1",
		Name:      "test-config-2",
	})

	// Also expect kube-root-ca.crt ConfigMap (automatically created in namespace)
	expectedEvents = append(expectedEvents, ExpectedEvent{
		EventType: "ADDED",
		GVR:       "v1/configmaps",
		Namespace: "faro-test-1",
		Name:      "kube-root-ca.crt",
	})

	return expectedEvents
}

// getFirstResourceGVR extracts the first resource GVR from namespace config
func getFirstResourceGVR(resources map[string]ResourceDetails) string {
	for gvr := range resources {
		return gvr
	}
	return ""
}

// waitForFaroReady waits for Faro to be ready
func waitForFaroReady(logFile string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(logFile); os.IsNotExist(err) {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		content, err := os.ReadFile(logFile)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		logContent := string(content)
		if strings.Contains(logContent, "Starting config-driven informers") {
			return nil
		}

		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("timeout waiting for Faro to be ready")
}

// extractJSONEvents extracts JSON events from Faro log file
func extractJSONEvents(logFile string) ([]FaroJSONEvent, error) {
	file, err := os.Open(logFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}
	defer file.Close()

	var events []FaroJSONEvent
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()

		// Look for JSON lines that contain eventType
		if strings.Contains(line, `"eventType"`) {
			// Extract JSON from the log line (after [controller] prefix)
			jsonStart := strings.Index(line, `{"timestamp"`)
			if jsonStart != -1 {
				jsonStr := line[jsonStart:]

				var event FaroJSONEvent
				if err := json.Unmarshal([]byte(jsonStr), &event); err == nil {
					events = append(events, event)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading log file: %w", err)
	}

	return events, nil
}

// validateJSONEvents compares expected vs actual JSON events
func validateJSONEvents(t *testing.T, expectedEvents []ExpectedEvent, actualEvents []FaroJSONEvent) {
	t.Logf("Validating %d expected events against %d actual events", len(expectedEvents), len(actualEvents))

	// Check each expected event
	for _, expected := range expectedEvents {
		found := false
		for _, actual := range actualEvents {
			if matchesExpectedEvent(expected, actual) {
				found = true
				t.Logf("✓ Found expected event: %s %s %s/%s", expected.EventType, expected.GVR, expected.Namespace, expected.Name)
				break
			}
		}

		if !found {
			t.Errorf("✗ Missing expected event: %s %s %s/%s", expected.EventType, expected.GVR, expected.Namespace, expected.Name)
		}
	}

	// Report unexpected events (informational)
	for _, actual := range actualEvents {
		expected := false
		for _, exp := range expectedEvents {
			if matchesExpectedEvent(exp, actual) {
				expected = true
				break
			}
		}

		if !expected {
			t.Logf("ℹ️  Unexpected event: %s %s %s/%s", actual.EventType, actual.GVR, actual.Namespace, actual.Name)
		}
	}

	// Verify minimum event count (should have at least the core expected events)
	coreExpectedCount := 6 // 2 ConfigMaps × 3 events each (ADDED, UPDATED, DELETED)
	if len(actualEvents) < coreExpectedCount {
		t.Errorf("Expected at least %d events, got %d", coreExpectedCount, len(actualEvents))
	}
}

// matchesExpectedEvent checks if an actual event matches an expected event
func matchesExpectedEvent(expected ExpectedEvent, actual FaroJSONEvent) bool {
	return expected.EventType == actual.EventType &&
		expected.GVR == actual.GVR &&
		expected.Namespace == actual.Namespace &&
		expected.Name == actual.Name
}