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

const (
	// Sized for a capture-everything config: enough that several hundred
	// informers establish in seconds rather than minutes, while still being a
	// limit rather than no limit.
	defaultClientQPS   = 200
	defaultClientBurst = 400
)

// KubernetesClient wraps the dynamic client and provides configuration
type KubernetesClient struct {
	Dynamic   dynamic.Interface
	Discovery discovery.DiscoveryInterface
	Config    *rest.Config
}

// NewKubernetesClient creates a Kubernetes client
// Automatically detects in-cluster config (when running as an operator)
// and falls back to kubeconfig file for out-of-cluster usage
func NewKubernetesClient() (*KubernetesClient, error) {
	// Try in-cluster config first (for operator deployments)
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fallback to kubeconfig file (for CLI/local usage)
		kubeconfigPath := os.Getenv("KUBECONFIG")
		if kubeconfigPath == "" {
			kubeconfigPath = filepath.Join(os.Getenv("HOME"), ".kube", "config")
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
		}
	}

	// Raise the client's own rate limit before anything uses it.
	//
	// client-go defaults to QPS 5 / Burst 10, which is sized for a controller
	// watching a handful of types. Faro builds ONE INFORMER PER (type x
	// namespace) pair and each needs an initial LIST, so a capture-everything
	// config - 480 informers, measured - queues 480 requests behind 5 per
	// second. Measured on that cluster: client-side throttling from 09:18:52 to
	// 09:21:03, with individual delays escalating 1.1s -> 11.1s -> 21.1s.
	//
	// Two minutes is not a slow start, it is a hole in the record. The addons
	// install during exactly that window, so whether a type's informer was
	// established before or after the thing it was meant to observe is decided
	// by where it sat in the queue, and nothing reports which.
	//
	// These are client-side limits only. The API server's own protection is API
	// Priority and Fairness, which is unaffected by this and is the thing that
	// should be deciding whether Faro is asking for too much.
	if config.QPS == 0 {
		config.QPS = defaultClientQPS
		config.Burst = defaultClientBurst
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
