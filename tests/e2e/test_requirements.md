# E2E Test Requirements

## Test Phase Communication

E2E tests must clearly communicate each phase during execution:

1. **PHASE 1: START MONITORING** - Launch Faro binary and wait for initialization
2. **PHASE 2: WORKING WITH MANIFESTS** - Apply manifests, update resources, delete resources
3. **PHASE 3: STOPPING MONITORING** - Stop Faro binary after all manifest work is complete
4. **PHASE 4: LOADING EVENTS JSON** - Load and analyze captured JSON events from Faro logs
5. **PHASE 5: COMPARING DATA** - Validate expected events were captured correctly

Each phase must be clearly marked with separators and descriptive log messages for easy debugging and progress tracking.

## E2E vs Integration Tests

- **E2E Tests**: Test the Faro binary as a black box using config files
- **Integration Tests**: Test the Faro library directly using Go code