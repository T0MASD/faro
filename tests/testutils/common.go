// Package testutils provides shared utilities for Faro test suites
package testutils

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FaroJSONEvent represents the structure of JSON events exported by Faro
// This is the canonical definition used across all test suites
type FaroJSONEvent struct {
	Timestamp string            `json:"timestamp"`
	EventType string            `json:"eventType"`
	GVR       string            `json:"gvr"`
	Namespace string            `json:"namespace,omitempty"`
	Name      string            `json:"name"`
	UID       string            `json:"uid,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// CreateKubernetesClients creates both standard and dynamic Kubernetes clients
func CreateKubernetesClients(t *testing.T) (kubernetes.Interface, dynamic.Interface) {
	t.Helper()
	
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fallback to kubeconfig
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
		}
		
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			t.Fatalf("Failed to create Kubernetes config: %v", err)
		}
	}
	
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("Failed to create Kubernetes client: %v", err)
	}
	
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		t.Fatalf("Failed to create dynamic client: %v", err)
	}
	
	return clientset, dynamicClient
}

// DeleteNamespace safely deletes a namespace with proper error handling
func DeleteNamespace(t *testing.T, client kubernetes.Interface, name string) {
	t.Helper()
	
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	err := client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		t.Logf("Warning: Failed to delete namespace %s: %v", name, err)
	} else {
		t.Logf("✅ Deleted namespace: %s", name)
	}
}

// ReadJSONEvents reads JSON events from Faro's dedicated JSON export files ONLY
func ReadJSONEvents(t *testing.T, logDir string) []FaroJSONEvent {
	t.Helper()
	
	// Find JSON export files - ONLY location we support
	jsonPattern := filepath.Join(logDir, "logs", "events-*.json")
	jsonFiles, err := filepath.Glob(jsonPattern)
	if err != nil || len(jsonFiles) == 0 {
		// Also try logDir directly as alternative location
		jsonPattern = filepath.Join(logDir, "events-*.json")
		jsonFiles, err = filepath.Glob(jsonPattern)
	}
	
	if err != nil || len(jsonFiles) == 0 {
		t.Fatalf("❌ ERROR: No JSON export files found in %s - json_export: true MUST be set in config passed to NewLogger. Tests MUST use JSON data for validation, never log parsing!", logDir)
	}
	
	// Use the most recent JSON export file
	mostRecentFile := jsonFiles[len(jsonFiles)-1]
	events := readJSONFromFile(t, mostRecentFile)
	if len(events) == 0 {
		t.Fatalf("❌ ERROR: JSON export file %s is empty or invalid - check that events are being properly exported", mostRecentFile)
	}
	
	return events
}

// readJSONFromFile reads and parses JSON events from a dedicated JSON export file
func readJSONFromFile(t *testing.T, filename string) []FaroJSONEvent {
	t.Helper()
	
	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("❌ ERROR: Failed to read JSON file %s: %v", filename, err)
	}
	
	var events []FaroJSONEvent
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		
		var event FaroJSONEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("❌ ERROR: Failed to parse JSON event in file %s, line: %s, error: %v", filename, line, err)
		}
		events = append(events, event)
	}
	
	return events
}


// Contains checks if a slice contains a string
func Contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// EnsureLogDir creates a log directory if it doesn't exist
func EnsureLogDir(t *testing.T, logDir string) {
	t.Helper()
	
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("Failed to create log directory %s: %v", logDir, err)
	}
}

// WaitForKubernetesResource waits for a Kubernetes resource to be ready/deleted
func WaitForKubernetesResource(t *testing.T, client kubernetes.Interface, namespace, resourceType, resourceName string, expectExists bool, timeout time.Duration) bool {
	t.Helper()
	
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			exists := checkResourceExists(client, namespace, resourceType, resourceName)
			if exists == expectExists {
				return true
			}
		}
	}
}

// checkResourceExists checks if a Kubernetes resource exists
func checkResourceExists(client kubernetes.Interface, namespace, resourceType, resourceName string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	switch resourceType {
	case "configmap":
		_, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, resourceName, metav1.GetOptions{})
		return err == nil
	case "namespace":
		_, err := client.CoreV1().Namespaces().Get(ctx, resourceName, metav1.GetOptions{})
		return err == nil
	case "secret":
		_, err := client.CoreV1().Secrets(namespace).Get(ctx, resourceName, metav1.GetOptions{})
		return err == nil
	default:
		return false
	}
}

// ApplyManifestWithWait applies a Kubernetes manifest and waits for resources to be ready
func ApplyManifestWithWait(t *testing.T, manifestFile string, timeout time.Duration) error {
	t.Helper()
	
	// Apply manifest
	applyCmd := exec.Command("kubectl", "apply", "-f", manifestFile)
	if err := applyCmd.Run(); err != nil {
		return fmt.Errorf("failed to apply manifest %s: %v", manifestFile, err)
	}
	
	// TODO: Add resource waiting logic based on manifest content
	// Use a shorter fixed delay
	time.Sleep(1 * time.Second)
	
	return nil
}

// DeleteManifestWithWait deletes a Kubernetes manifest and waits for cleanup
func DeleteManifestWithWait(t *testing.T, manifestFile string, timeout time.Duration) error {
	t.Helper()
	
	// Delete manifest
	deleteCmd := exec.Command("kubectl", "delete", "-f", manifestFile, "--ignore-not-found=true")
	if err := deleteCmd.Run(); err != nil {
		return fmt.Errorf("failed to delete manifest %s: %v", manifestFile, err)
	}
	
	// TODO: Add resource cleanup waiting logic
	// Use a shorter fixed delay
	time.Sleep(1 * time.Second)
	
	return nil
}