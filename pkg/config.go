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
	// Fields are object paths copied into the emitted event, e.g. "reason" or
	// "status.phase". Per-GVR rather than global: see pkg/fields.go for why.
	Fields []string `yaml:"fields,omitempty"`
}

// NamespaceConfig defines namespace and its resources to watch (namespace-centric format)
type NamespaceConfig struct {
	NameSelector string                     `yaml:"name_selector"` // Exact namespace name for server-side filtering
	Resources   map[string]ResourceDetails `yaml:"resources"`     // Map of GVR to resource config
}

// ResourceConfig defines a resource-centric configuration
type ResourceConfig struct {
	GVR            string   `yaml:"gvr"`                        // Group/Version/Resource identifier
	Scope          Scope    `yaml:"scope,omitempty"`           // Explicitly define scope (Cluster or Namespaced)
	NamespaceNames []string `yaml:"namespace_names,omitempty"` // Exact namespace names only (for server-side filtering)
	NameSelector   string   `yaml:"name_selector,omitempty"`   // Exact name for resource name filtering (server-side)
	LabelSelector  string   `yaml:"label_selector,omitempty"`  // Kubernetes label selector for SERVER-SIDE filtering only (e.g. "app=faro-test")
	// Fields are object paths copied into the emitted event, e.g. "reason",
	// "involvedObject.name" or "status.containerStatuses.restartCount". A path
	// that is absent on an object is omitted rather than emitted empty.
	Fields []string `yaml:"fields,omitempty"`
}

// NormalizedConfig is the unified data structure used internally by the controller.
// This represents the normalized form that both configuration formats are converted to.
type NormalizedConfig struct {
	GVR               string          // Group/Version/Resource identifier
	ResourceDetails   ResourceDetails // Resource matching details (SERVER-SIDE only)
	NamespaceNames []string        // Literal namespace names only (for server-side filtering)
	NameSelector   string          // Exact name for resource name filtering (server-side)
	LabelSelector     string          // Kubernetes label selector for SERVER-SIDE filtering only (e.g. "app=faro-test")
	Fields            []string        // Object paths copied into the emitted event
}

// MetricsConfig defines Prometheus metrics configuration
type MetricsConfig struct {
	Enabled    bool   `yaml:"enabled"`              // Enable Prometheus metrics
	Port       int    `yaml:"port"`                 // Port for metrics HTTP server (default: 8080)
	Path       string `yaml:"path"`                 // Metrics endpoint path (default: /metrics)
	BindAddr   string `yaml:"bind_addr"`            // Bind address (default: 0.0.0.0)
}

// Config represents the minimalist Faro configuration supporting both formats
type Config struct {
	OutputDir       string            `yaml:"output_dir"`       // Directory for output files and logs
	LogLevel        string            `yaml:"log_level"`        // Log level: debug, info, warning, error, fatal
	AutoShutdownSec int               `yaml:"auto_shutdown_sec"` // Auto-shutdown timeout in seconds (0 = run indefinitely)
	JsonExport      bool              `yaml:"json_export,omitempty"` // Enable JSON event export to separate file

	// How often the capture file is fsynced: "interval" (the default, once a
	// second) or "record" (after every record).
	//
	// "record" was the only behaviour and it caps the collector at one record
	// per fsync. On a network filesystem that is ~100ms, so ~8 records/s no
	// matter how many workers run, and above that the work queue grows without
	// bound (#35). "interval" trades the last second of capture on a hard kill
	// for a cap set by the disk's throughput rather than its latency.
	//
	// It is the default because under load it loses LESS: a backlog lives only
	// in memory, so a collector that is minutes behind loses minutes of events
	// if its pod is rescheduled, where an interval sync that keeps up loses at
	// most a second if the node is destroyed outright.
	JsonSync        string            `yaml:"json_sync,omitempty"`   // "interval" (default) or "record"

	// When to start a new capture file.
	//
	// On a chunked network filesystem EVERY fsync appends a chunk, and fsync
	// latency grows with the number of chunks in the file. Measured on a
	// SeaweedFS CSI mount - one 300-byte append plus fsync, 7164 times into a
	// single file:
	//
	//   syncs    0-999  1000-1999  2000-2999  4000-4999  6000-6999
	//   median    20ms     39ms       67ms      104ms      167ms
	//
	// The same test against an emptyDir stays flat at 11-14ms, which is what
	// makes this chunk accumulation rather than the file merely getting
	// larger. The curve is super-linear: a sync passes 500ms in roughly six
	// hours at one per second, and would reach the sync interval itself inside
	// a day - at which point the lag the asynchronous writer removed comes
	// back.
	//
	// Rotation is the bound, because a new file starts with no chunks. It also
	// bounds what one damaged or unsynced file can cost.
	//
	// The interval defaults to an hour rather than to off. Off would mean the
	// default configuration degrades on the storage this is deployed on, and
	// only someone who had read the issue would be spared it. An hour caps a
	// file at ~3600 chunks, which the table above puts around 80ms. Records
	// default to off: the interval already bounds the chunk count, and this is
	// for bounding file SIZE on a collector that writes hard.
	JsonRotateInterval string `yaml:"json_rotate_interval,omitempty"` // "" = 1h, "0" = never
	JsonRotateRecords  int    `yaml:"json_rotate_records,omitempty"`  // 0 = never

	// Where the SQLite capture goes. "" uses <output_dir>/logs/capture.db,
	// which is the DEFAULT: there is always a queryable capture.
	//
	// One database, reopened in place across restarts rather than a new file
	// per start. Safe because the Deployment runs Recreate so one collector
	// writes at a time, and measured: a hard VM kill with a 4.1MB live WAL on
	// a SeaweedFS RWX volume left the database opening clean, every
	// acknowledged commit present, nothing torn.
	//
	// "off" disables it.
	SqlitePath string `yaml:"sqlite_path,omitempty"`

	// The JSON-lines capture, which is now OPTIONAL and off unless asked for.
	// It remains because CI, the e2e suite and the drive-test harness read it,
	// and because it is the format that has survived every failure thrown at
	// it. json_export: true keeps writing it alongside the database.
	Metrics         MetricsConfig     `yaml:"metrics,omitempty"`     // Prometheus metrics configuration
	
	// Simple configuration formats
	Namespaces      []NamespaceConfig `yaml:"namespaces,omitempty"`  // Simple namespace format
	Resources       []ResourceConfig  `yaml:"resources,omitempty"`   // Simple resource format
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
	if c.OutputDir == "" {
		return "" // Console-only mode, no log files
	}
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

// Normalize provides simple config structure conversion - supports both formats
func (c *Config) Normalize() (map[string][]NormalizedConfig, error) {
	normalizedMap := make(map[string][]NormalizedConfig)

	// Simple namespace format conversion
	for _, nsConfig := range c.Namespaces {
		for gvr, details := range nsConfig.Resources {
			normalizedMap[gvr] = append(normalizedMap[gvr], NormalizedConfig{
				GVR:            gvr,
				NamespaceNames: []string{nsConfig.NameSelector},
				LabelSelector:  details.LabelSelector,
				Fields:         allowedFields(gvr, details.Fields),
			})
		}
	}

	// Simple resource format conversion
	for _, resConfig := range c.Resources {
		normalizedMap[resConfig.GVR] = append(normalizedMap[resConfig.GVR], NormalizedConfig{
			GVR:            resConfig.GVR,
			NamespaceNames: resConfig.NamespaceNames,
			NameSelector:   resConfig.NameSelector,
			LabelSelector:  resConfig.LabelSelector,
			Fields:         allowedFields(resConfig.GVR, resConfig.Fields),
		})
	}
	
	if len(normalizedMap) == 0 {
		return nil, fmt.Errorf("no resources configured")
	}

	return normalizedMap, nil
}

// allowedFields drops any configured path that pkg/fields.go forbids for this
// resource. Dropped silently-but-deliberately at normalization time so that a
// denied path cannot reach an informer at all; ExtractFields checks again on
// the emit path, because a library user may build a controller without ever
// calling Normalize.
func allowedFields(gvr string, fields []string) []string {
	if len(fields) == 0 {
		return nil
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f == "" || FieldDenied(gvr, f) {
			continue
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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