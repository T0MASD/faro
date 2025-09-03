package faro

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"
)

// Scope defines whether a resource is cluster-scoped or namespace-scoped
type Scope string

const (
	ClusterScope    Scope = "Cluster"
	NamespaceScope  Scope = "Namespaced"
)

// ResourceDetails defines what resources to watch within a namespace (legacy format)
type ResourceDetails struct {
	LabelSelector string `yaml:"label_selector,omitempty"` // Kubernetes label selector for SERVER-SIDE filtering only (e.g. "app=faro-test")
}

// NamespaceConfig defines namespace and its resources to watch (namespace-centric format)
type NamespaceConfig struct {
	NamePattern string                      `yaml:"name_pattern"` // Regex pattern for namespace names
	Resources   map[string]ResourceDetails `yaml:"resources"`    // Map of GVR to resource config
}

// ResourceConfig defines a resource-centric configuration
type ResourceConfig struct {
	GVR               string   `yaml:"gvr"`                         // Group/Version/Resource identifier
	Scope             Scope    `yaml:"scope,omitempty"`            // Explicitly define scope (Cluster or Namespaced)
	NamespacePatterns []string `yaml:"namespace_patterns,omitempty"` // Literal namespace names only (for server-side filtering)
	NamePattern       string   `yaml:"name_pattern,omitempty"`     // Exact name for resource name filtering (server-side)
	LabelSelector     string   `yaml:"label_selector,omitempty"`   // Kubernetes label selector for SERVER-SIDE filtering only (e.g. "app=faro-test")
}

// NormalizedConfig is the unified data structure used internally by the controller.
// This represents the normalized form that both configuration formats are converted to.
type NormalizedConfig struct {
	GVR               string          // Group/Version/Resource identifier
	ResourceDetails   ResourceDetails // Resource matching details (SERVER-SIDE only)
	NamespacePatterns []string        // Literal namespace names only (for server-side filtering)
	NamePattern       string          // Exact name for resource name filtering (server-side)
	LabelSelector     string          // Kubernetes label selector for SERVER-SIDE filtering only (e.g. "app=faro-test")
}

// Config represents the minimalist Faro configuration supporting both formats
type Config struct {
	OutputDir       string            `yaml:"output_dir"`       // Directory for output files and logs
	LogLevel        string            `yaml:"log_level"`        // Log level: debug, info, warning, error, fatal
	AutoShutdownSec int               `yaml:"auto_shutdown_sec"` // Auto-shutdown timeout in seconds (0 = run indefinitely)
	JsonExport      bool              `yaml:"json_export,omitempty"` // Enable JSON event export to separate file
	
	// Configuration formats - only one should be populated
	Namespaces      []NamespaceConfig `yaml:"namespaces,omitempty"`  // Namespace-centric format (legacy)
	Resources       []ResourceConfig  `yaml:"resources,omitempty"`   // Resource-centric format (new)
}

// LoadConfig loads configuration from YAML file or command line arguments
func LoadConfig() (*Config, error) {
	return LoadConfigWithVersion("dev", "none", "unknown", "unknown")
}

// LoadConfigWithVersion loads configuration with version information for help/version display
func LoadConfigWithVersion(version, commit, date, builtBy string) (*Config, error) {
	config := &Config{}
	
	// Define command line flags
	var configFile string
	flag.StringVar(&configFile, "config", "", "Path to YAML configuration file")
	flag.StringVar(&config.OutputDir, "output-dir", "./output", "Directory for output files and logs")
	flag.StringVar(&config.LogLevel, "log-level", "info", "Log level (debug, info, warning, error, fatal)")
	flag.IntVar(&config.AutoShutdownSec, "auto-shutdown", 0, "Auto-shutdown timeout in seconds (0 = run indefinitely)")
	
	// Add help and version flags
	var showHelp bool
	var showVersion bool
	flag.BoolVar(&showHelp, "help", false, "Show help")
	flag.BoolVar(&showHelp, "h", false, "Show help (shorthand)")
	flag.BoolVar(&showVersion, "version", false, "Show version information")
	
	// Parse flags
	flag.Parse()
	
	// Show version if requested
	if showVersion {
		fmt.Printf("Faro %s\n", version)
		fmt.Printf("  commit: %s\n", commit)
		fmt.Printf("  built: %s\n", date)
		fmt.Printf("  built by: %s\n", builtBy)
		os.Exit(0)
	}
	
	// Show help if requested
	if showHelp {
		printUsage()
		os.Exit(0)
	}
	
	// Load from YAML file if specified
	if configFile != "" {
		if err := config.LoadFromYAML(configFile); err != nil {
			return nil, fmt.Errorf("failed to load config from YAML: %w", err)
		}
	}
	
	// Validate configuration
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}
	
	// Ensure output directory exists
	if err := os.MkdirAll(config.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}
	
	return config, nil
}

// LoadFromYAML loads configuration from a YAML file
func (c *Config) LoadFromYAML(filename string) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}
	
	if err := yaml.Unmarshal(data, c); err != nil {
		return fmt.Errorf("failed to parse YAML config: %w", err)
	}
	
	return nil
}

// Validate validates the configuration
func (c *Config) Validate() error {
	// Validate log level
	validLevels := map[string]bool{
		"debug":   true,
		"info":    true,
		"warning": true,
		"error":   true,
		"fatal":   true,
	}
	
	if !validLevels[c.LogLevel] {
		return fmt.Errorf("invalid log level '%s', must be one of: debug, info, warning, error, fatal", c.LogLevel)
	}
	
	// Validate output directory path
	if c.OutputDir == "" {
		return fmt.Errorf("output directory cannot be empty")
	}
	
	// Convert to absolute path for consistency
	absPath, err := filepath.Abs(c.OutputDir)
	if err != nil {
		return fmt.Errorf("invalid output directory path: %w", err)
	}
	c.OutputDir = absPath
	
	return nil
}

// GetLogLevel returns the log level as an integer for klog
func (c *Config) GetLogLevel() int {
	switch c.LogLevel {
	case "debug":
		return -1 // klog uses negative values for debug/trace
	case "info":
		return 0
	case "warning":
		return 1
	case "error":
		return 2
	case "fatal":
		return 3
	default:
		return 0 // Default to info
	}
}

// GetLogDir returns the directory where log files should be stored
func (c *Config) GetLogDir() string {
	return filepath.Join(c.OutputDir, "logs")
}

// REMOVED: All client-side filtering functions have been eliminated from Faro core

// GetMatchingResources returns all GVRs configured for a namespace
func (nsConfig *NamespaceConfig) GetMatchingResources() []string {
	var gvrs []string
	for gvr := range nsConfig.Resources {
		gvrs = append(gvrs, gvr)
	}
	return gvrs
}

// Normalize takes either config format and converts it to a single internal structure.
// Returns a map[string][]NormalizedConfig where the key is the GVR string and the value
// is a slice of all NormalizedConfig objects that should monitor that GVR.
func (c *Config) Normalize() (map[string][]NormalizedConfig, error) {
	normalizedMap := make(map[string][]NormalizedConfig)

	if len(c.Namespaces) > 0 {
		// Process namespace-centric config
		for _, nsConfig := range c.Namespaces {
			for gvr, details := range nsConfig.Resources {
				// Normalize into a single structure (SERVER-SIDE filtering only)
				normalizedMap[gvr] = append(normalizedMap[gvr], NormalizedConfig{
					GVR:               gvr,
					ResourceDetails:   details,
					NamespacePatterns: []string{nsConfig.NamePattern}, // Literal namespace names only
					LabelSelector:     details.LabelSelector, // SERVER-SIDE filtering only
				})
			}
		}
	}
	
	if len(c.Resources) > 0 {
		// Process resource-centric config
		for _, resConfig := range c.Resources {
			// Handle the case where namespaces are not specified (e.g., cluster-scoped)
			namespacePatterns := resConfig.NamespacePatterns
			if len(namespacePatterns) == 0 {
				if resConfig.Scope == ClusterScope {
					// For cluster-scoped resources, use empty pattern to indicate cluster scope
					namespacePatterns = []string{""}
				} else {
					// Default to "all namespaces" for namespace-scoped resources without explicit patterns
					namespacePatterns = []string{""}
				}
			}
			
			normalizedMap[resConfig.GVR] = append(normalizedMap[resConfig.GVR], NormalizedConfig{
				GVR: resConfig.GVR,
				ResourceDetails: ResourceDetails{
					LabelSelector: resConfig.LabelSelector, // SERVER-SIDE filtering only
				},
				NamespacePatterns: namespacePatterns, // Literal namespace names only
				NamePattern:       resConfig.NamePattern, // SERVER-SIDE name filtering
				LabelSelector:     resConfig.LabelSelector, // SERVER-SIDE filtering only
			})
		}
	}
	
	if len(normalizedMap) == 0 {
		return nil, fmt.Errorf("no valid configuration found - must have either 'namespaces' or 'resources' section")
	}

	return normalizedMap, nil
}

// printUsage prints command line usage information
func printUsage() {
	fmt.Fprintf(os.Stderr, "Faro - Kubernetes Resource Monitoring Library\n\n")
	fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "Options:\n")
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, "\nExamples:\n")
	fmt.Fprintf(os.Stderr, "  %s --config=examples/minimal-config.yaml\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s --output-dir=/tmp/faro --log-level=debug\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s --auto-shutdown=300 --config=test.yaml\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -h\n", os.Args[0])
}