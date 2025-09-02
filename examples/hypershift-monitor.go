package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	faro "github.com/T0MASD/faro/pkg"
)

// HyperShiftMonitor handles dynamic discovery and monitoring of HyperShift clusters
type HyperShiftMonitor struct {
	client             *faro.KubernetesClient
	logger             *faro.Logger
	clusterControllers map[string]*faro.Controller
	mu                 sync.RWMutex
	detectedClusters   map[string]bool
	clusterName        string
	logDir             string
}

// ClusterHandler handles events for a specific cluster's namespaces
type ClusterHandler struct {
	ClusterID string
	Logger    *faro.Logger
}

// StructuredLogEntry represents a structured log entry for Kubernetes resources
type StructuredLogEntry struct {
	Timestamp  string            `json:"timestamp"`
	Level      string            `json:"level"`
	Message    string            `json:"message"`
	Kubernetes KubernetesContext `json:"kubernetes"`
}

// KubernetesContext contains Kubernetes-specific metadata
type KubernetesContext struct {
	ClusterID    string            `json:"cluster_id"`
	Namespace    string            `json:"namespace,omitempty"`
	ResourceType string            `json:"resource_type"`
	ResourceName string            `json:"resource_name"`
	Action       string            `json:"action"`
	UID          string            `json:"uid,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

// logStructured logs a structured JSON entry
func (c *ClusterHandler) logStructured(action, gvr, namespace, resourceName, uid string, labels map[string]string) {
	entry := StructuredLogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Level:     "info",
		Message:   fmt.Sprintf("Kubernetes resource %s", strings.ToLower(action)),
		Kubernetes: KubernetesContext{
			ClusterID:    c.ClusterID,
			Namespace:    namespace,
			ResourceType: gvr,
			ResourceName: resourceName,
			Action:       action,
			UID:          uid,
			Labels:       labels,
		},
	}
	
	jsonBytes, err := json.Marshal(entry)
	if err != nil {
		// Fallback to simple logging if JSON marshaling fails
		c.Logger.Error("cluster-handler", fmt.Sprintf("Failed to marshal JSON log: %v", err))
		return
	}
	
	c.Logger.Info("cluster-handler", string(jsonBytes))
}

func (c *ClusterHandler) OnMatched(event faro.MatchedEvent) error {
	// Extract common metadata
	uid := string(event.Object.GetUID())
	labels := event.Object.GetLabels()
	
	if event.GVR == "v1/events" {
		// For v1/events, extract the actual resource info from the event
		eventObj := event.Object
		involvedObj, found := eventObj.Object["involvedObject"].(map[string]interface{})
		if found {
			kind, _ := involvedObj["kind"].(string)
			apiVersion, _ := involvedObj["apiVersion"].(string)
			name, _ := involvedObj["name"].(string)
			
			// Construct GVR from apiVersion and kind
			var gvr string
			if apiVersion == "v1" {
				gvr = "v1/" + strings.ToLower(kind) + "s"
			} else {
				gvr = apiVersion + "/" + strings.ToLower(kind) + "s"
			}
			
			// Get the namespace where the event occurred
			eventNamespace := event.Object.GetNamespace()
			c.logStructured(event.EventType, gvr, eventNamespace, name, uid, labels)
		} else {
			// Fallback to original format if involvedObject not found
			namespace := event.Object.GetNamespace()
			c.logStructured(event.EventType, event.GVR, namespace, event.Object.GetName(), uid, labels)
		}
	} else {
		// For non-event resources, use direct resource information
		namespace := event.Object.GetNamespace()
		c.logStructured(event.EventType, event.GVR, namespace, event.Object.GetName(), uid, labels)
	}
	return nil
}

// OnMatched handles discovery events for parent namespaces
func (h *HyperShiftMonitor) OnMatched(event faro.MatchedEvent) error {
	fmt.Printf("🔍 DISCOVERY EVENT: %s %s %s\n", event.EventType, event.GVR, event.Object.GetName())
	
	if event.GVR == "v1/namespaces" {
		labels := event.Object.GetLabels()
		fmt.Printf("   Labels: %+v\n", labels)
		
		if labels["api.openshift.com/name"] == h.clusterName {
			parentNS := event.Object.GetName() // e.g., ocm-staging-2l1k7t68urkho8hsj5h5hhnicv4997lp
			clusterID := strings.TrimPrefix(parentNS, "ocm-staging-") // e.g., 2l1k7t68urkho8hsj5h5hhnicv4997lp

			fmt.Printf("🎯 MATCHED HYPERSHIFT PARENT: %s (cluster: %s)\n", parentNS, clusterID)

			h.mu.Lock()
			if !h.detectedClusters[clusterID] {
				h.detectedClusters[clusterID] = true
				h.mu.Unlock()
				fmt.Printf("🔍 Discovered HyperShift cluster: %s (parent: %s)\n", clusterID, parentNS)
				h.createClusterController(clusterID, h.clusterName, h.logDir)
			} else {
				h.mu.Unlock()
				fmt.Printf("   Cluster %s already detected, skipping\n", clusterID)
			}
		} else {
			fmt.Printf("   No %s label found\n", h.clusterName)
		}
	}
	return nil
}

// createClusterController creates a dedicated controller for monitoring a specific cluster's namespaces
func (h *HyperShiftMonitor) createClusterController(clusterID, clusterName, logDir string) {
	parentNS := "ocm-staging-" + clusterID
	childNS := parentNS + "-" + clusterName
	klusterletNS := "klusterlet-" + clusterID
	
	fmt.Printf("📡 Creating controller for cluster %s namespaces:\n", clusterID)
	fmt.Printf("   - Parent: %s\n", parentNS)
	fmt.Printf("   - Child: %s\n", childNS)
	fmt.Printf("   - Klusterlet: %s\n", klusterletNS)

	// Create monitoring configuration for all expected namespaces (Faro will wait for them)
	resources := []faro.ResourceConfig{
		// Monitor all 3 namespaces
		{GVR: "v1/namespaces", Scope: faro.ClusterScope, NamePattern: fmt.Sprintf("^%s$", parentNS)},
		{GVR: "v1/namespaces", Scope: faro.ClusterScope, NamePattern: fmt.Sprintf("^%s$", childNS)},
		{GVR: "v1/namespaces", Scope: faro.ClusterScope, NamePattern: fmt.Sprintf("^%s$", klusterletNS)},
		
		{GVR: "v1/events", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", parentNS)}},
		{GVR: "v1/events", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "v1/events", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", klusterletNS)}},
		
		// Monitor parent namespace resources (ONLY silent resources - others covered by events)
		{GVR: "cert-manager.io/v1/certificaterequests", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", parentNS)}},
		{GVR: "cert-manager.io/v1/certificates", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", parentNS)}},
		{GVR: "acme.cert-manager.io/v1/challenges", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", parentNS)}},
		{GVR: "acme.cert-manager.io/v1/orders", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", parentNS)}},
		{GVR: "hypershift.openshift.io/v1beta1/hostedclusters", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", parentNS)}},
		{GVR: "hypershift.openshift.io/v1beta1/nodepools", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", parentNS)}},
		{GVR: "operators.coreos.com/v1alpha1/clusterserviceversions", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", parentNS)}},
		{GVR: "events.k8s.io/v1/events", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", parentNS)}},
		{GVR: "v1/events", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", parentNS)}},
		
		// Monitor klusterlet-specific resources (ONLY silent resources - others covered by events)
		{GVR: "operators.coreos.com/v1alpha1/clusterserviceversions", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", klusterletNS)}},
		{GVR: "policy.open-cluster-management.io/v1/policies", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", klusterletNS)}},
		{GVR: "events.k8s.io/v1/events", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", klusterletNS)}},
		{GVR: "v1/events", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", klusterletNS)}},
		
		// Monitor child namespace resources (ONLY silent resources - others covered by events)
		{GVR: "v1/endpoints", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "v1/persistentvolumeclaims", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "apps/v1/statefulsets", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "avo.openshift.io/v1alpha2/vpcendpoints", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "batch/v1/cronjobs", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "cluster.x-k8s.io/v1beta1/clusters", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "cluster.x-k8s.io/v1beta1/machinedeployments", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "cluster.x-k8s.io/v1beta1/machinehealthchecks", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "cluster.x-k8s.io/v1beta1/machines", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "cluster.x-k8s.io/v1beta1/machinesets", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "events.k8s.io/v1/events", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "hypershift.openshift.io/v1beta1/controlplanecomponents", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "hypershift.openshift.io/v1beta1/hostedcontrolplanes", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "image.openshift.io/v1/imagestreams", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "image.openshift.io/v1/imagestreamtags", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "image.openshift.io/v1/imagetags", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "infrastructure.cluster.x-k8s.io/v1beta2/awsclusters", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "infrastructure.cluster.x-k8s.io/v1beta2/awsmachines", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "infrastructure.cluster.x-k8s.io/v1beta2/awsmachinetemplates", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "machine.openshift.io/v1beta1/machinehealthchecks", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "monitoring.coreos.com/v1/prometheusrules", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "networking.k8s.io/v1/networkpolicies", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "operators.coreos.com/v1alpha1/clusterserviceversions", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "package-operator.run/v1alpha1/objectdeployments", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "package-operator.run/v1alpha1/objectsetphases", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "package-operator.run/v1alpha1/objectsets", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "package-operator.run/v1alpha1/objecttemplates", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "package-operator.run/v1alpha1/packages", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "policy/v1/poddisruptionbudgets", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
		{GVR: "v1/events", Scope: faro.NamespaceScope, NamespacePatterns: []string{fmt.Sprintf("^%s$", childNS)}},
	}

	config := &faro.Config{
		OutputDir: logDir, // Use cluster-specific log directory
		LogLevel:  "info",
		Resources: resources,
	}

	controller := faro.NewController(h.client, h.logger, config) // Use the same logger
	controller.AddEventHandler(&ClusterHandler{ClusterID: clusterID, Logger: h.logger})

	h.mu.Lock()
	h.clusterControllers[clusterID] = controller
	h.mu.Unlock()

	go func() {
		if err := controller.Start(); err != nil {
			h.logger.Error("cluster-controller", fmt.Sprintf("Failed to start cluster controller for %s: %v", clusterID, err))
		}
		fmt.Printf("✅ Controller started for cluster: %s\n", clusterID)
	}()
}

func main() {
	// Parse command line flags
	clusterName := flag.String("cluster", "ocpe2e-yrjrgsci", "Cluster name to monitor (e.g., toda-slsre, ocpe2e-yrjrgsci)")
	flag.Parse()

	fmt.Println("🚀 HyperShift Cluster Namespace Monitor")
	fmt.Printf("📡 Monitoring clusters with api.openshift.com/name=%s\n", *clusterName)

	// Create Faro client
	client, err := faro.NewKubernetesClient()
	if err != nil {
		log.Fatalf("Failed to create Faro client: %v", err)
	}

	// Create logger with cluster name prefix
	logDir := fmt.Sprintf("./logs/%s", *clusterName)
	logger, err := faro.NewLogger(logDir)
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	// Discovery config - monitors parent namespaces by label
	discoveryConfig := &faro.Config{
		OutputDir: logDir,
		LogLevel:  "debug", // INCREASED TO DEBUG FOR TROUBLESHOOTING
		Resources: []faro.ResourceConfig{
			{
				GVR:           "v1/namespaces",
				Scope:         faro.ClusterScope,
				LabelSelector: fmt.Sprintf("api.openshift.com/name=%s", *clusterName), // Monitor parent namespaces by label
			},
		},
	}

	// Create discovery controller
	discoveryController := faro.NewController(client, logger, discoveryConfig)
	
	// Create monitor with discovery handler
	monitor := &HyperShiftMonitor{
		client:             client,
		logger:             logger,
		clusterControllers: make(map[string]*faro.Controller),
		detectedClusters:   make(map[string]bool),
		clusterName:        *clusterName,
		logDir:             logDir,
	}
	
	discoveryController.AddEventHandler(monitor)

	// Start discovery controller
	if err := discoveryController.Start(); err != nil {
		log.Fatalf("Failed to start discovery controller: %v", err)
	}

	fmt.Println("✅ HyperShift monitor started. Press Ctrl+C to stop.")
	
	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	
	// Shutdown all controllers
	fmt.Println("\n🔄 Stopping all controllers...")
	monitor.mu.RLock()
	for clusterID, controller := range monitor.clusterControllers {
		fmt.Printf("   Stopping controller for cluster: %s\n", clusterID)
		controller.Stop()
	}
	monitor.mu.RUnlock()
	
	discoveryController.Stop()
	
	// Give controllers time to shutdown
	time.Sleep(2 * time.Second)
	fmt.Println("✅ Shutdown complete")
}