package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	faro "github.com/T0MASD/faro/pkg"
)

func main() {
	// Load configuration from YAML file (vanilla Faro way)
	// Use simple-test-1.yaml config for test9
	config := &faro.Config{}
	if err := config.LoadFromYAML("configs/simple-test-1.yaml"); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	
	// Create Kubernetes client
	client, err := faro.NewKubernetesClient()
	if err != nil {
		log.Fatalf("Failed to create Kubernetes client: %v", err)
	}
	
	// Create logger
	logger, err := faro.NewLogger(config.GetLogDir())
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()
	
	// Create Faro controller (NO event handlers - vanilla functionality)
	controller := faro.NewController(client, logger, config)
	
	// Start Faro
	if err := controller.Start(); err != nil {
		log.Fatalf("Failed to start controller: %v", err)
	}
	
	// Handle auto-shutdown
	if config.AutoShutdownSec > 0 {
		go func() {
			time.Sleep(time.Duration(config.AutoShutdownSec) * time.Second)
			os.Exit(0)
		}()
	}
	
	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	
	controller.Stop()
}