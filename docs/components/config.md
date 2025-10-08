# Configuration Component

The Configuration component provides **simple YAML parsing** without complex business logic interpretation. Complex configuration processing is left to library users.

## Philosophy

**Faro Core Provides**: Basic YAML structure parsing and simple normalization
**Library Users Implement**: Complex interpretation, pattern matching, and business rules

## Supported Formats

Faro supports two simple configuration formats that are normalized to a unified internal structure:

### 1. Namespace-Centric Format
Groups resources by namespace:

```yaml
output_dir: "./logs"
log_level: "info"
json_export: true

namespaces:
  - name_pattern: "production"
    resources:
      "v1/configmaps":
        label_selector: "app=nginx"
      "batch/v1/jobs": {}
      "v1/events": {}
  - name_pattern: "staging"  
    resources:
      "v1/configmaps":
        label_selector: "app=web"
```

### 2. Resource-Centric Format
Groups namespaces by resource type:

```yaml
output_dir: "./logs"
log_level: "info"
json_export: true

resources:
  - gvr: "v1/configmaps"
    namespace_names: ["production", "staging"]
    label_selector: "app=nginx"
  - gvr: "batch/v1/jobs"
    namespace_names: ["production"]
  - gvr: "v1/namespaces"
    namespace_names: [""]  # Cluster-scoped (empty namespace)
```

## Configuration Fields

### Global Settings
```yaml
output_dir: "./logs"           # Directory for logs and JSON export
log_level: "info"              # Log level: debug, info, warning, error
json_export: true              # Enable structured JSON event export
auto_shutdown_sec: 120         # Auto-shutdown timeout (0 = run indefinitely)
```

### Resource Configuration
```yaml
# Simple resource specification
gvr: "v1/configmaps"                    # Group/Version/Resource
namespace_names: ["prod", "staging"]    # Target namespaces (exact names only)
label_selector: "app=nginx,tier=web"    # Kubernetes label selector
name_selector: "app-config"             # Exact resource name (no patterns)
```

## Normalization Process

Both configuration formats are converted to a unified internal structure:

```go
type NormalizedConfig struct {
    GVR            string   // Group/Version/Resource identifier
    NamespaceNames []string // Target namespace names
    NameSelector   string   // Exact resource name filter
    LabelSelector  string   // Kubernetes label selector
}
```

### Simple Conversion Logic
```go
func (c *Config) Normalize() (map[string][]NormalizedConfig, error) {
    normalizedMap := make(map[string][]NormalizedConfig)

    // Simple namespace format conversion
    for _, nsConfig := range c.Namespaces {
        for gvr, details := range nsConfig.Resources {
            normalizedMap[gvr] = append(normalizedMap[gvr], NormalizedConfig{
                GVR:            gvr,
                NamespaceNames: []string{nsConfig.NameSelector},
                LabelSelector:  details.LabelSelector,
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
        })
    }
    
    if len(normalizedMap) == 0 {
        return nil, fmt.Errorf("no resources configured")
    }

    return normalizedMap, nil
}
```

## What Faro Core Does NOT Do

### No Complex Interpretation
- **No Regex Processing**: Pattern matching left to library users
- **No Default Values**: Missing configuration causes errors
- **No Validation Logic**: Complex validation implemented by users
- **No Business Rules**: Policy decisions made by library users

### No Fallback Logic
```go
// Faro does NOT do this:
if config.Invalid {
    // Apply default configuration
    // Start default monitoring
    // Hide the error
}

// Faro DOES do this:
if len(normalizedMap) == 0 {
    return nil, fmt.Errorf("no resources configured")
}
```

## Library User Responsibilities

### 1. Complex Configuration Processing
Library users implement advanced configuration logic:

```go
type AdvancedConfig struct {
    *faro.Config
    // Add complex fields
    NamePatterns    []string `yaml:"name_patterns"`
    LabelPatterns   []string `yaml:"label_patterns"`
    BusinessRules   []Rule   `yaml:"business_rules"`
}

func (a *AdvancedConfig) ProcessAdvanced() error {
    // Implement regex processing
    // Apply business rules
    // Generate dynamic configurations
    // Convert to simple Faro config
    return a.convertToFaroConfig()
}
```

### 2. Dynamic Configuration Generation
Library users can generate configurations at runtime:

```go
type ConfigGenerator struct {
    baseConfig *faro.Config
}

func (g *ConfigGenerator) GenerateFromCRDs() (*faro.Config, error) {
    // Discover CRDs
    // Generate resource configurations
    // Apply business logic
    // Return simple Faro config
}

func (g *ConfigGenerator) GenerateFromEvents() (*faro.Config, error) {
    // Process events for GVR discovery
    // Extract namespace patterns
    // Generate monitoring configuration
    // Return simple Faro config
}
```

### 3. Configuration Validation
Library users implement validation logic:

```go
type ConfigValidator struct{}

func (v *ConfigValidator) Validate(config *faro.Config) error {
    // Check business rules
    // Validate resource accessibility
    // Verify namespace existence
    // Apply security policies
    return nil
}
```

## Usage Examples

### Basic Usage
```go
// Simple configuration loading
config, err := faro.LoadConfig()
if err != nil {
    log.Fatal("Configuration error:", err)
}

// No fallbacks - errors must be handled
controller := faro.NewController(client, logger, config)
```

### Advanced Usage with Custom Processing
```go
// Load base configuration
baseConfig, err := faro.LoadConfig()
if err != nil {
    log.Fatal("Base configuration error:", err)
}

// Apply business logic
processor := &ConfigProcessor{baseConfig: baseConfig}
processedConfig, err := processor.ApplyBusinessRules()
if err != nil {
    log.Fatal("Configuration processing error:", err)
}

// Use processed configuration
controller := faro.NewController(client, logger, processedConfig)
```

### Dynamic Configuration Updates
```go
// Initial configuration
controller := faro.NewController(client, logger, initialConfig)
controller.Start()

// Runtime configuration updates (library user implements)
configWatcher := &ConfigWatcher{controller: controller}
go configWatcher.WatchForChanges()

// Add new resources dynamically
newResources := []faro.ResourceConfig{
    {GVR: "batch/v1/jobs", NamespaceNames: []string{"production"}},
}
controller.AddResources(newResources)
controller.StartInformers()
```

## Error Handling

### Strict Error Propagation
Configuration errors are **never masked** with defaults:

```go
// Configuration loading
config, err := faro.LoadConfig()
if err != nil {
    // Error must be handled - no fallbacks provided
    return fmt.Errorf("configuration failed: %w", err)
}

// Normalization
normalized, err := config.Normalize()
if err != nil {
    // Invalid configuration causes immediate error
    return fmt.Errorf("normalization failed: %w", err)
}
```

### No Hidden Behavior
All configuration processing is explicit:

```go
// Faro does NOT hide configuration problems
func (c *Config) Normalize() (map[string][]NormalizedConfig, error) {
    // No default resources
    // No fallback namespaces  
    // No hidden business logic
    
    if len(normalizedMap) == 0 {
        return nil, fmt.Errorf("no resources configured")
    }
    return normalizedMap, nil
}
```

## Integration with Examples

### Workload Monitor Configuration
The workload monitor example shows how library users implement complex configuration:

```go
// Base Faro configuration (simple)
baseConfig := &faro.Config{
    Resources: []faro.ResourceConfig{
        {GVR: "v1/namespaces", NamespaceNames: []string{""}},
        {GVR: "v1/events", NamespaceNames: workloadNamespaces},
    },
}

// Business logic adds complexity
workloadConfig := &faro.Config{
    Resources: []faro.ResourceConfig{
        {GVR: "batch/v1/jobs", NamespaceNames: workloadNamespaces},
        {GVR: "v1/configmaps", NamespaceNames: workloadNamespaces},
        {GVR: "v1/events", NamespaceNames: workloadNamespaces},
    },
}
```

## Testing

### Unit Tests
- **Simple Parsing**: Test YAML loading and basic normalization
- **Error Cases**: Verify proper error propagation
- **Format Support**: Test both namespace and resource formats

### Integration Tests  
- **Real Configurations**: Test with actual YAML files
- **Complex Scenarios**: Library user implementations with advanced logic
- **Dynamic Updates**: Runtime configuration changes

The Configuration component provides **simple, reliable YAML parsing** while leaving complex interpretation and business logic to library users, maintaining clean separation of concerns.