# E2E Test Requirements

## Overview
Create comprehensive E2E tests using `sigs.k8s.io/e2e-framework` to validate Faro's event capture capabilities against an existing Kubernetes cluster.

## Test Workflow
Each E2E test must follow this exact sequence:

1. **Parse Faro Config** - Read YAML config file to understand what Faro should monitor
2. **Generate Expected Data** - Based on config, create list of expected JSON events
3. **Start Faro Binary** - Launch Faro with `json_export: true` using `os/exec`
4. **Execute Kubernetes Actions** - Create/Update/Delete resources using e2e-framework client
5. **Stop Faro Binary** - Terminate Faro process
6. **Extract JSON Events** - Parse Faro logs to extract JSON events (not CONFIG messages)
7. **Validate Events** - Compare expected vs actual JSON events

## Technical Requirements

### Dependencies
- `sigs.k8s.io/e2e-framework` - Kubernetes E2E testing framework
- `gopkg.in/yaml.v2` - YAML config parsing
- `encoding/json` - JSON event parsing
- `os/exec` - Binary execution
- Standard Go libraries only

### No Faro Library Imports
- **MUST NOT** import any packages from `github.com/T0MASD/faro`
- **MUST** use Faro binary directly via `os/exec`
- **MUST** parse config files as YAML
- **MUST** extract JSON events from log files

### Test Structure
```go
package e2e

import (
    "context"
    "encoding/json"
    "os"
    "os/exec"
    "testing"
    
    "gopkg.in/yaml.v2"
    "sigs.k8s.io/e2e-framework/pkg/env"
    "sigs.k8s.io/e2e-framework/pkg/features"
    // k8s client libraries for resource management
)

var testenv env.Environment

func TestMain(m *testing.M) {
    testenv = env.New()
    // Setup/teardown for existing cluster
    os.Exit(testenv.Run(m))
}

func TestFaroE2E(t *testing.T) {
    feature := features.New("Faro E2E Test").
        Assess("should capture all expected events", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
            // 1. Parse config
            // 2. Generate expected events
            // 3. Start Faro binary
            // 4. Execute K8s actions
            // 5. Stop Faro
            // 6. Extract & validate JSON
            return ctx
        }).Feature()
    
    testenv.Test(t, feature)
}
```

### Data Structures

#### Faro Config (YAML parsing)
```go
type FaroConfig struct {
    OutputDir       string `yaml:"output_dir"`
    LogLevel        string `yaml:"log_level"`
    AutoShutdownSec int    `yaml:"auto_shutdown_sec"`
    JsonExport      bool   `yaml:"json_export"`
    Namespaces      []NamespaceConfig `yaml:"namespaces,omitempty"`
    Resources       []ResourceConfig  `yaml:"resources,omitempty"`
}

type NamespaceConfig struct {
    NamePattern string                       `yaml:"name_pattern"`
    Resources   map[string]ResourceDetails   `yaml:"resources"`
}

type ResourceConfig struct {
    GVR           string            `yaml:"gvr"`
    Scope         string            `yaml:"scope"`
    NamePattern   string            `yaml:"name_pattern,omitempty"`
    LabelSelector string            `yaml:"label_selector,omitempty"`
    Namespaces    []string          `yaml:"namespaces,omitempty"`
}
```

#### Expected Event (generated from config)
```go
type ExpectedEvent struct {
    EventType string            `json:"eventType"`  // ADDED, UPDATED, DELETED
    GVR       string            `json:"gvr"`        // v1/configmaps, v1/services, etc.
    Namespace string            `json:"namespace"`  // namespace name (empty for cluster-scoped)
    Name      string            `json:"name"`       // resource name
    Labels    map[string]string `json:"labels,omitempty"`
}
```

#### Actual Event (from Faro JSON logs)
```go
type FaroJSONEvent struct {
    Timestamp string            `json:"timestamp"`
    EventType string            `json:"eventType"`
    GVR       string            `json:"gvr"`
    Namespace string            `json:"namespace,omitempty"`
    Name      string            `json:"name"`
    UID       string            `json:"uid,omitempty"`
    Labels    map[string]string `json:"labels,omitempty"`
}
```

### JSON Event Extraction
Extract JSON events from Faro logs using pattern matching:
```bash
# Extract JSON lines containing "eventType"
grep '"eventType":' faro.log | sed 's/.*\[controller\] //' | jq .
```

### Validation Logic
- **Event Count**: Verify expected number of events captured
- **Event Types**: Ensure ADDED, UPDATED, DELETED events are present
- **Resource Matching**: Verify GVR, namespace, name match expected
- **Label Validation**: Check labels are captured correctly
- **Timing**: Events should appear in logical order (ADDED → UPDATED → DELETED)

## Test Coverage

### Test 1: Namespace-Centric ConfigMap
- **Config**: `simple-test-1.yaml`
- **Resources**: ConfigMaps in `faro-test-1` namespace
- **Expected Events**: 6 events (3 ADDED, 2 UPDATED, 2 DELETED for 2 ConfigMaps)

### Test 2: Resource-Centric Services
- **Config**: `simple-test-2.yaml`
- **Resources**: Services across multiple namespaces
- **Expected Events**: Service lifecycle events

### Test 3: Label-Based Selection
- **Config**: `simple-test-3.yaml`
- **Resources**: Resources with specific labels
- **Expected Events**: Only labeled resources captured

### Test 4: Mixed Resource Types
- **Config**: `simple-test-4.yaml`
- **Resources**: ConfigMaps, Services, Secrets
- **Expected Events**: All resource types captured

### Test 5: Namespace Monitoring Only
- **Config**: `simple-test-5.yaml`
- **Resources**: Namespace lifecycle events
- **Expected Events**: Namespace ADDED/UPDATED/DELETED

## Success Criteria
- All expected events are captured as JSON
- No unexpected events are captured
- Event data matches Kubernetes resource metadata
- DELETE events are properly captured (critical requirement)
- JSON structure is valid and parseable
- Tests run reliably against existing cluster
- No Faro library dependencies in test code

## Error Handling
- Faro binary build failures
- Kubernetes connection issues
- Missing or malformed JSON events
- Timeout waiting for events
- Resource creation/deletion failures

## Performance Requirements
- Tests should complete within 2 minutes per test
- Faro should capture events within 5 seconds of K8s action
- JSON extraction should be efficient for large log files