package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	faro "github.com/T0MASD/faro/pkg"
)

// ExampleEventHandler demonstrates how to handle Faro events in your application
type ExampleEventHandler struct {
	name string
}

func (e *ExampleEventHandler) OnMatched(event faro.MatchedEvent) error {
	fmt.Printf("[%s] Received event: %s %s %s\n",
		e.name,
		event.EventType,
		event.GVR,
		event.Key)
	
	// You can access the full Kubernetes object
	if event.Object != nil {
		labels := event.Object.GetLabels()
		if len(labels) > 0 {
			fmt.Printf("[%s] Object labels: %v\n", e.name, labels)
		}
	}
	
	return nil
}

func main() {
	fmt.Println("🚀 Faro Library Usage Example")
	
	// 1. Create configuration programmatically
	config := &faro.Config{
		OutputDir:  "./logs",
		LogLevel:   "info",
		Resources: []faro.ResourceConfig{
			{
				GVR:               "v1/configmaps",
				Scope:             faro.NamespaceScope,
				NamespacePatterns: []string{"default", "kube-system"},
				NamePattern:       ".*",
			},
			{
				GVR:         "v1/namespaces",
				Scope:       faro.ClusterScope,
				NamePattern: ".*test.*",
			},
		},
	}
	
	// 2. Create Kubernetes client
	client, err := faro.NewKubernetesClient()
	if err != nil {
		log.Fatalf("Failed to create Kubernetes client: %v", err)
	}
	
	// 3. Create logger
	// Create config for logger
	config := &faro.Config{OutputDir: "./logs", JsonExport: true}
	logger, err := faro.NewLogger(config)
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()
	
	// 4. Create Faro controller
	controller := faro.NewController(client, logger, config)
	
	// 5. Register event handlers
	controller.AddEventHandler(&ExampleEventHandler{name: "Handler-1"})
	controller.AddEventHandler(&ExampleEventHandler{name: "Handler-2"})
	
	// 6. Start Faro
	if err := controller.Start(); err != nil {
		log.Fatalf("Failed to start controller: %v", err)
	}
	
	builtin, dynamic := controller.GetActiveInformers()
	fmt.Printf("✅ Faro started with %d builtin + %d dynamic informers\n", builtin, dynamic)
	fmt.Println("📡 Listening for Kubernetes events...")
	fmt.Println("💡 Try creating/updating/deleting ConfigMaps in 'default' or 'kube-system' namespaces")
	fmt.Println("💡 Try creating/updating/deleting namespaces with 'test' in the name")
	
	// 7. Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	
	fmt.Println("\n🛑 Shutting down...")
	controller.Stop()
	fmt.Println("✅ Shutdown complete")
}