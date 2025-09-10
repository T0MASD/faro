package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	faro "github.com/T0MASD/faro/pkg"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ResourceWorker interface for handling specific resource types
type ResourceWorker interface {
	HandleEvent(ctx context.Context, event faro.MatchedEvent) error
	Handles() []string
	Name() string
}

// WorkerDispatcher manages multiple resource-specific workers
type WorkerDispatcher struct {
	workers    map[string]ResourceWorker
	workChan   chan faro.MatchedEvent
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

func NewWorkerDispatcher() *WorkerDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &WorkerDispatcher{
		workers:  make(map[string]ResourceWorker),
		workChan: make(chan faro.MatchedEvent, 1000),
		ctx:      ctx,
		cancel:   cancel,
	}
}

func (wd *WorkerDispatcher) RegisterWorker(worker ResourceWorker) {
	for _, gvr := range worker.Handles() {
		wd.workers[gvr] = worker
	}
	
	// Start worker goroutine
	wd.wg.Add(1)
	go wd.runWorker(worker)
}

func (wd *WorkerDispatcher) runWorker(worker ResourceWorker) {
	defer wd.wg.Done()
	
	fmt.Printf("🔧 Starting worker: %s\n", worker.Name())
	
	for {
		select {
		case <-wd.ctx.Done():
			return
		case event := <-wd.workChan:
			// Check if this worker handles this GVR
			handles := false
			for _, gvr := range worker.Handles() {
				if gvr == event.GVR {
					handles = true
					break
				}
			}
			
			if handles {
				if err := worker.HandleEvent(wd.ctx, event); err != nil {
					log.Printf("Worker %s failed to handle event %s: %v",
						worker.Name(), event.Key, err)
				}
			}
		}
	}
}

// OnMatched implements the faro.EventHandler interface
func (wd *WorkerDispatcher) OnMatched(event faro.MatchedEvent) error {
	select {
	case wd.workChan <- event:
		return nil
	default:
		return fmt.Errorf("worker dispatcher is busy")
	}
}

func (wd *WorkerDispatcher) Shutdown() {
	wd.cancel()
	wd.wg.Wait()
}

// ConfigMapWorker handles ConfigMap events
type ConfigMapWorker struct{}

func (cw *ConfigMapWorker) Name() string {
	return "configmap-worker"
}

func (cw *ConfigMapWorker) Handles() []string {
	return []string{"v1/configmaps"}
}

func (cw *ConfigMapWorker) HandleEvent(ctx context.Context, event faro.MatchedEvent) error {
	fmt.Printf("📋 [ConfigMap Worker] %s %s\n", event.EventType, event.Key)
	
	if event.EventType == "ADDED" {
		// Simulate getting ConfigMap data
		if data, found, _ := unstructured.NestedStringMap(event.Object.Object, "data"); found {
			fmt.Printf("   📝 ConfigMap data keys: %v\n", getKeys(data))
		}
	} else if event.EventType == "DELETED" {
		fmt.Printf("   🗑️  ConfigMap deleted: %s\n", event.Key)
	}
	
	return nil
}

// NamespaceWorker handles Namespace events
type NamespaceWorker struct{}

func (nw *NamespaceWorker) Name() string {
	return "namespace-worker"
}

func (nw *NamespaceWorker) Handles() []string {
	return []string{"v1/namespaces"}
}

func (nw *NamespaceWorker) HandleEvent(ctx context.Context, event faro.MatchedEvent) error {
	fmt.Printf("🏠 [Namespace Worker] %s %s\n", event.EventType, event.Key)
	
	if event.EventType == "ADDED" {
		phase, _, _ := unstructured.NestedString(event.Object.Object, "status", "phase")
		fmt.Printf("   📊 Namespace phase: %s\n", phase)
	}
	
	return nil
}

// Helper function to get keys from map
func getKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func main() {
	fmt.Println("🚀 Faro Worker Dispatcher Example")
	
	// 1. Create configuration
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
	
	// 2. Create Faro components
	client, err := faro.NewKubernetesClient()
	if err != nil {
		log.Fatalf("Failed to create Kubernetes client: %v", err)
	}
	
	// Create config for logger
	config := &faro.Config{OutputDir: "./logs", JsonExport: true}
	logger, err := faro.NewLogger(config)
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()
	
	controller := faro.NewController(client, logger, config)
	
	// 3. Create and configure worker dispatcher
	dispatcher := NewWorkerDispatcher()
	
	// Register resource-specific workers
	dispatcher.RegisterWorker(&ConfigMapWorker{})
	dispatcher.RegisterWorker(&NamespaceWorker{})
	
	// Connect Faro to worker dispatcher
	controller.AddEventHandler(dispatcher)
	
	// 4. Start everything
	if err := controller.Start(); err != nil {
		log.Fatalf("Failed to start controller: %v", err)
	}
	
	builtin, dynamic := controller.GetActiveInformers()
	fmt.Printf("✅ Faro started with %d builtin + %d dynamic informers\n", builtin, dynamic)
	fmt.Println("🔧 Worker dispatcher ready with specialized handlers")
	fmt.Println("📡 Listening for Kubernetes events...")
	
	// 5. Wait for shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	
	fmt.Println("\n🛑 Shutting down...")
	controller.Stop()
	dispatcher.Shutdown()
	fmt.Println("✅ Shutdown complete")
}