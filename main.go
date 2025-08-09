package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/klog/v2"
	faro "github.com/kubetube/faro/pkg"
)

func main() {
	// Initialize klog first with default settings
	klog.InitFlags(nil)
	defer klog.Flush()

	// Create initial logger with info level for startup messages
	tempLogger, err := faro.NewLogger("./logs")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create initial logger: %v\n", err)
		os.Exit(1)
	}
	
	tempLogger.Info("main", "Faro v2 initializing...")

	// Load minimalistic config from command line args
	config, err := faro.LoadConfig()
	if err != nil {
		tempLogger.Error("main", fmt.Sprintf("Error loading config: %v", err))
		tempLogger.Shutdown()
		os.Exit(1)
	}

	// Set klog verbosity based on config log level
	if config.LogLevel == "debug" {
		flag.Set("v", "1") // Enable klog verbosity level 1 for debug messages
	}
	
	tempLogger.Info("main", fmt.Sprintf("Config loaded, switching to configured log directory: %s", config.GetLogDir()))
	tempLogger.Shutdown()

	// Create final logger with config-specified log directory
	logger, err := faro.NewLogger(config.GetLogDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Shutdown()
	
	// Log configuration
	logger.Info("main", fmt.Sprintf("Faro v2 starting up..."))
	logger.Info("main", fmt.Sprintf("Output directory: %s", config.OutputDir))
	logger.Info("main", fmt.Sprintf("Log level: %s", config.LogLevel))
	logger.Info("main", fmt.Sprintf("Log directory: %s", config.GetLogDir()))
	
	// Test different log levels
	logger.Info("main", "This is an info message")
	logger.Warning("main", "This is a warning message")
	logger.Error("main", "This is an error message")
	
	// Create Kubernetes client
	k8sClient, err := faro.NewKubernetesClient()
	if err != nil {
		logger.Error("main", fmt.Sprintf("Failed to create Kubernetes client: %v", err))
		return
	}
	
	logger.Info("main", "Kubernetes client created successfully")
	
	// Create sophisticated multi-layered informer controller
	controller := faro.NewController(k8sClient, logger, config)
	
	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	
	// Set up graceful shutdown function
	performGracefulShutdown := func(reason string) {
		logger.Info("main", fmt.Sprintf("Initiating graceful shutdown: %s", reason))
		
		// Create a context with timeout for graceful shutdown
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		
		// Create a channel to signal when shutdown is complete
		shutdownComplete := make(chan bool, 1)
		
		// Perform shutdown in a goroutine
		go func() {
			if controller != nil {
				logger.Info("main", "Stopping controller...")
				controller.Stop()
				logger.Info("main", "Controller stopped successfully")
			}
			shutdownComplete <- true
		}()
		
		// Wait for either graceful shutdown completion or timeout
		select {
		case <-shutdownComplete:
			logger.Info("main", "Graceful shutdown completed successfully")
		case <-shutdownCtx.Done():
			logger.Warning("main", "Graceful shutdown timeout exceeded, forcing exit")
		case <-sigChan:
			logger.Warning("main", "Second signal received, forcing immediate exit")
			os.Exit(1)
		}
	}
	
	// Start the controller
	if err := controller.Start(); err != nil {
		logger.Error("main", fmt.Sprintf("Failed to start controller: %v", err))
		return
	}
	
	builtin, dynamic := controller.GetActiveInformers()
	logger.Info("main", fmt.Sprintf("Controller started with %d builtin + %d dynamic informers", builtin, dynamic))
	
	// Handle auto-shutdown configuration or wait for signals
	if config.AutoShutdownSec > 0 {
		timeout := time.After(time.Duration(config.AutoShutdownSec) * time.Second)
		logger.Info("main", fmt.Sprintf("Waiting for shutdown signal or auto-shutdown timeout (%ds)...", config.AutoShutdownSec))
		
		select {
		case sig := <-sigChan:
			performGracefulShutdown(fmt.Sprintf("signal received (%s)", sig))
		case <-timeout:
			performGracefulShutdown(fmt.Sprintf("auto-shutdown timeout (%ds) reached", config.AutoShutdownSec))
		}
	} else {
		logger.Info("main", "Running indefinitely - waiting for shutdown signal (Ctrl+C)...")
		sig := <-sigChan
		performGracefulShutdown(fmt.Sprintf("signal received (%s)", sig))
	}
	
	logger.Info("main", "Faro v2 shutdown complete")
}