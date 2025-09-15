# Integration Test Requirements

## Test Phase Communication

Integration tests must clearly communicate each phase during execution:

1. **PHASE 1: START MONITORING** - Initialize Faro controllers and begin resource monitoring
2. **PHASE 2: WORKING WITH MANIFESTS** - Create namespaces, deploy resources, delete resources, wait for events
3. **PHASE 3: STOPPING MONITORING** - Stop monitoring after all manifest work is complete
4. **PHASE 4: LOADING EVENTS JSON** - Load and analyze captured JSON events
5. **PHASE 5: COMPARING DATA** - Validate data integrity and expected annotations

Each phase must be clearly marked with separators and descriptive log messages for easy debugging and progress tracking.

## E2E vs Integration Tests

- **E2E Tests**: Test the Faro binary as a black box using config files
- **Integration Tests**: Test the Faro library directly using Go code