# Workload Analysis Requirements

## Step 1: Workload Detection

### Objective
Extract unique workloads from production events based on namespace labeling selectors.

### Detection Criteria
- **Source**: `prod_events.json` file
- **Resource Filter**: `gvr == "v1/namespaces"`
- **Label Requirement**: `api.openshift.com/name` label must be present
- **Namespace Regex**: Extract workload ID from `ocm-staging-{workload_id}` regex

### Required Output Format
```json
{
  "workload_name": "<value from api.openshift.com/name label>",
  "workload_id": "<extracted from namespace name>"
}
```

### JQ Command Template
```bash
jq -r 'select(.gvr == "v1/namespaces") | 
       select(.labels["api.openshift.com/name"] != null) | 
       {
         workload_name: .labels["api.openshift.com/name"], 
         workload_id: (.name | capture("ocm-staging-(?<id>.+)").id // "no_match")
       }' prod_events.json | jq -s 'unique_by(.workload_id) | sort_by(.workload_id)'
```

## Step 2: Workload Namespace Identification

### Objective
For each workload ID, identify all associated namespaces that belong to that workload.

### Detection Criteria
- **Source**: `prod_events.json` file
- **Resource Filter**: `gvr == "v1/namespaces"`
- **Regex Match**: Namespace name contains the workload ID
- **Input**: Workload IDs from Step 1

### Required Output Format
```json
{
  "workload_id": "<workload_id>",
  "namespaces": ["<namespace1>", "<namespace2>", "..."]
}
```

### JQ Command Template
```bash
# For a specific workload_id
jq -r --arg workload_id "WORKLOAD_ID_HERE" '
  select(.gvr == "v1/namespaces") | 
  select(.name | test($workload_id)) | 
  .name' prod_events.json | sort -u
```

## Step 3: Event GVR Identification by Namespace

### Objective
For each namespace, identify all event GVRs (Group/Version/Resource) that occurred within that namespace.

### Detection Criteria
- **Source**: `prod_events.json` file
- **Filter**: Events with matching namespace field
- **Input**: Namespace names from Step 2

### Required Output Format
```json
{
  "namespace": "<namespace_name>",
  "gvrs": ["<gvr1>", "<gvr2>", "..."]
}
```

### JQ Command Template
```bash
# For a specific namespace
jq -r --arg namespace "NAMESPACE_NAME_HERE" '
  select(.namespace == $namespace) | 
  .gvr' prod_events.json | sort -u
```

## Step 4: GVR Activity Analysis

### Objective
Analyze GVR activity within each namespace by counting events and event types.

### Sub-step 4a: GVR Event Count by Namespace

#### Detection Criteria
- **Source**: `prod_events.json` file
- **Filter**: Events with matching namespace and GVR
- **Input**: Namespace and GVR from Steps 2 and 3

#### Required Output Format
```json
{
  "namespace": "<namespace_name>",
  "gvr": "<gvr_name>",
  "event_count": <total_events>
}
```

#### JQ Command Template
```bash
# Count events for specific namespace and GVR
jq -r --arg namespace "NAMESPACE_NAME_HERE" --arg gvr "GVR_NAME_HERE" '
  select(.namespace == $namespace and .gvr == $gvr)' prod_events.json | wc -l
```

### Sub-step 4b: Event Type Distribution

#### Detection Criteria
- **Source**: `prod_events.json` file
- **Filter**: Events with matching namespace and GVR
- **Group by**: eventType field

#### Required Output Format
```json
{
  "namespace": "<namespace_name>",
  "gvr": "<gvr_name>",
  "event_types": {
    "ADDED": <count>,
    "UPDATED": <count>,
    "DELETED": <count>
  }
}
```

#### JQ Command Template
```bash
# Count event types for specific namespace and GVR
jq -r --arg namespace "NAMESPACE_NAME_HERE" --arg gvr "GVR_NAME_HERE" '
  select(.namespace == $namespace and .gvr == $gvr) | 
  .eventType' prod_events.json | sort | uniq -c
```