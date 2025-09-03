package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	faro "github.com/T0MASD/faro/pkg"
)

// DynamicDiscoveryHandler handles discovery of parent namespaces and creates targeted controllers
type DynamicDiscoveryHandler struct {
	client             *faro.KubernetesClient
	logger             *faro.Logger
	targetControllers  map[string]*faro.Controller
	mu                 sync.RWMutex
	discoveredTargets  map[string]bool
}

func (d *DynamicDiscoveryHandler) OnMatched(event faro.MatchedEvent) error {
	if event.GVR == "v1/namespaces" && event.EventType == "ADDED" {
		labels := event.Object.GetLabels()
		parentNS := event.Object.GetName()
		
		// Check if this namespace has the next-namespace label
		if nextNS, exists := labels["next-namespace"]; exists {
			d.mu.Lock()
			if !d.discoveredTargets[nextNS] {
				d.discoveredTargets[nextNS] = true
				d.mu.Unlock()
				
				fmt.Printf("🔍 Parent namespace %s detected, creating controller for target: %s\n", parentNS, nextNS)
				d.createTargetController(nextNS)
			} else {
				d.mu.Unlock()
			}
		}
	}
	return nil
}

func (d *DynamicDiscoveryHandler) createTargetController(targetNS string) {
	config := &faro.Config{
		OutputDir: "./logs",
		LogLevel:  "info",
		Resources: []faro.ResourceConfig{
			{
				GVR:         "v1/namespaces",
				Scope:       faro.ClusterScope,
				NamePattern: fmt.Sprintf("^%s$", targetNS), // Exact match for target namespace
			},
		},
	}
	
	controller := faro.NewController(d.client, d.logger, config)
	controller.AddEventHandler(&TargetNamespaceHandler{TargetNS: targetNS})
	
	go func() {
		if err := controller.Start(); err != nil {
			log.Printf("Failed to start target controller for %s: %v", targetNS, err)
		} else {
			fmt.Printf("✅ Target controller started for namespace: %s\n", targetNS)
		}
	}()
	
	d.mu.Lock()
	d.targetControllers[targetNS] = controller
	d.mu.Unlock()
}

// TargetNamespaceHandler handles events from the dynamically created target controllers
type TargetNamespaceHandler struct {
	TargetNS string
}

func (t *TargetNamespaceHandler) OnMatched(event faro.MatchedEvent) error {
	fmt.Printf("[TARGET-%s] %s %s %s\n", 
		t.TargetNS, 
		event.EventType, 
		event.GVR, 
		event.Key)
	return nil
}

func main() {
	fmt.Println("🚀 Test10 - Dynamic Namespace Discovery Library Test")
	
	// Discovery config - monitors specific namespace faro-testa by name
	discoveryConfig := &faro.Config{
		OutputDir: "./logs",
		LogLevel:  "info",
		Resources: []faro.ResourceConfig{
			{
				GVR:         "v1/namespaces",
				Scope:       faro.ClusterScope,
				NamePattern: "^faro-testa$", // Watch specific namespace by name
			},
		},
	}
	
	client, err := faro.NewKubernetesClient()
	if err != nil {
		log.Fatalf("Failed to create Kubernetes client: %v", err)
	}
	
	logger, err := faro.NewLogger("./logs")
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()
	
	// Create discovery controller
	discoveryController := faro.NewController(client, logger, discoveryConfig)
	
	// Create dynamic handler
	handler := &DynamicDiscoveryHandler{
		client:            client,
		logger:            logger,
		targetControllers: make(map[string]*faro.Controller),
		discoveredTargets: make(map[string]bool),
	}
	
	discoveryController.AddEventHandler(handler)
	
	if err := discoveryController.Start(); err != nil {
		log.Fatalf("Failed to start discovery controller: %v", err)
	}
	
	builtin, dynamic := discoveryController.GetActiveInformers()
	fmt.Printf("✅ Discovery controller started with %d builtin + %d dynamic informers\n", builtin, dynamic)
	fmt.Println("📡 Waiting for faro-testa namespace...")
	
	// Run for 30 seconds to observe dynamic behavior
	time.Sleep(30 * time.Second)
	
	fmt.Println("🛑 Test10 completed - Dynamic namespace discovery demonstrated")
}