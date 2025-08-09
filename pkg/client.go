package faro

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// KubernetesClient wraps the dynamic client and provides configuration
type KubernetesClient struct {
	Dynamic   dynamic.Interface
	Discovery discovery.DiscoveryInterface
	Config    *rest.Config
}

// NewKubernetesClient creates a new Kubernetes client
func NewKubernetesClient() (*KubernetesClient, error) {
	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fallback to kubeconfig file
		kubeconfigPath := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
		}
	}

	// Create dynamic client
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	// Create discovery client
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}

	client := &KubernetesClient{
		Dynamic:   dynamicClient,
		Discovery: discoveryClient,
		Config:    config,
	}

	return client, nil
}

