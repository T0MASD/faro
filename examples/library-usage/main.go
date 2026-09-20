// Faro as a library: configure it in Go, handle events, and hear whether each
// informer actually listed.
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	faro "github.com/T0MASD/faro/pkg"
)

// ExampleEventHandler receives every event that matched the configuration.
type ExampleEventHandler struct {
	name string
}

func (e *ExampleEventHandler) OnMatched(event faro.MatchedEvent) error {
	// EventTime is when Faro observed the event. Timestamp is the object's
	// creationTimestamp, which is the same on every update, so a timeline is
	// built from EventTime.
	fmt.Printf("[%s] %s %s %s at %s\n",
		e.name, event.EventType, event.GVR, event.Key,
		event.EventTime.Format("15:04:05"))

	if event.Object != nil {
		if labels := event.Object.GetLabels(); len(labels) > 0 {
			fmt.Printf("[%s]   labels: %v\n", e.name, labels)
		}
	}
	return nil
}

// ExampleInformerStatusHandler separates the three ways a watcher can report
// nothing: an empty result, a refusal, and a type the endpoint does not serve.
// Without it all three look identical, because OnMatched never fires for any.
type ExampleInformerStatusHandler struct{}

func (s *ExampleInformerStatusHandler) OnInformerSynced(gvr, namespace string, resourceCount int) {
	where := namespace
	if where == "" {
		where = "cluster-wide"
	}
	fmt.Printf("✅ synced %s (%s), holding %d object(s)\n", gvr, where, resourceCount)
}

func (s *ExampleInformerStatusHandler) OnInformerFailed(gvr, namespace string, err error) {
	where := namespace
	if where == "" {
		where = "cluster-wide"
	}
	fmt.Printf("❌ %s (%s) did not list: %v\n", gvr, where, err)
}

func main() {
	fmt.Println("🚀 Faro Library Usage Example")

	// One config drives the controller and the logger.
	//
	// NameSelector and NamespaceNames are exact names used for server-side
	// filtering, not patterns. Leaving NameSelector unset matches every object
	// of the type, which is what these two entries want.
	config := &faro.Config{
		OutputDir:  "./logs",
		LogLevel:   "info",
		JsonExport: true,
		Resources: []faro.ResourceConfig{
			{
				GVR:            "v1/configmaps",
				Scope:          faro.NamespaceScope,
				NamespaceNames: []string{"default", "kube-system"},
			},
			{
				GVR:   "v1/namespaces",
				Scope: faro.ClusterScope,
			},
		},
	}

	client, err := faro.NewKubernetesClient()
	if err != nil {
		log.Fatalf("Failed to create Kubernetes client: %v", err)
	}

	logger, err := faro.NewLogger(config)
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	controller := faro.NewController(client, logger, config)

	controller.AddEventHandler(&ExampleEventHandler{name: "Handler-1"})
	controller.AddEventHandler(&ExampleEventHandler{name: "Handler-2"})
	controller.AddInformerStatusHandler(&ExampleInformerStatusHandler{})

	if err := controller.Start(); err != nil {
		log.Fatalf("Failed to start controller: %v", err)
	}

	configured, dynamic := controller.GetActiveInformers()
	fmt.Printf("✅ Faro started with %d configured + %d dynamic informers\n", configured, dynamic)
	fmt.Println("📡 Listening for Kubernetes events...")
	fmt.Println("💡 Create or edit a ConfigMap in 'default' or 'kube-system'")
	fmt.Println("💡 Create a namespace")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\n🛑 Shutting down...")
	controller.Stop()
	fmt.Println("✅ Shutdown complete")
}
