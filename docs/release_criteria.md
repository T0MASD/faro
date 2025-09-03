# Faro Release Criteria

## Release Validation Actions

### 1. Run make test to execute unit, integration and e2e tests, capture the output
- [ ] Execute full test suite
- [ ] Capture complete test output
- [ ] Verify all tests pass with zero failures

### 2. For each unit, integration and e2e tests read and capture faro*.log and events*.json
- [ ] Parse test stdout to identify FARO_LOG_FILE and FARO_JSON_FILE paths for each test
- [ ] Read and capture all faro*.log content from unit tests (if any)
- [ ] Read and capture all faro*.log content from integration tests
- [ ] Read and capture all faro*.log content from e2e tests
- [ ] Read and capture all events*.json content from all tests with JSON export enabled
- [ ] Ensure no log files are missed by using stdout identification instead of find commands

### 3. Build comprehensive understanding of faro operation from test run
- [ ] Analyze Faro initialization sequence and timing from logs
- [ ] Document API resource discovery results from logs
- [ ] Document server-side filtering evidence from logs
- [ ] Document event processing pipeline behavior from logs
- [ ] Document JSON export format and content from logs
- [ ] Document graceful shutdown behavior from logs

### 4. Validate server-side filtering implementation
- [ ] Verify FieldSelector is applied for name_pattern filtering in controller logs
- [ ] Verify LabelSelector is applied for label-based filtering in controller logs
- [ ] Confirm no client-side filtering occurs in Faro core from test evidence

### 5. Validate readiness callback mechanism
- [ ] Verify SetReadyCallback function works correctly from integration test logs
- [ ] Verify IsReady() returns correct state from test evidence
- [ ] Confirm callback is triggered after controller initialization from logs

### 6. Validate JSON export functionality
- [ ] Verify JSON events contain all required fields (timestamp, action, resource details)
- [ ] Verify JSON format is valid and parseable from events*.json files
- [ ] Confirm JSON export can be enabled/disabled via configuration

### 7. Validate graceful shutdown behavior
- [ ] Verify controller stops cleanly when context is cancelled from logs
- [ ] Verify all goroutines terminate properly during shutdown from logs
- [ ] Confirm no resource leaks during shutdown process

### 8. Validate configuration normalization
- [ ] Verify config.Normalize() handles all resource types correctly from test logs
- [ ] Verify namespace-centric vs resource-centric configurations work from test evidence
- [ ] Confirm invalid configurations are rejected with proper error messages

### 9. Validate Kubernetes client integration
- [ ] Verify dynamic client connects to all required API groups from logs
- [ ] Verify informers are created for all configured resources from logs
- [ ] Confirm client handles API server connection issues gracefully

### 10. Validate event processing pipeline
- [ ] Verify events are captured for CREATE, UPDATE, DELETE operations from logs
- [ ] Verify event timestamps are accurate and consistent from JSON exports
- [ ] Confirm event processing doesn't drop or duplicate events

### 11. Validate docs/components/controller.md with accordance to test results and pkg/controller.go
- [ ] Validate controller component documentation based on test evidence and source code

### 12. Validate docs/components/client.md with accordance to test results and pkg/client.go
- [ ] Validate client component documentation against test evidence and source code

### 13. Validate docs/components/config.md with accordance to test results and pkg/config.go
- [ ] Validate config component documentation against test evidence and source code

### 14. Validate docs/components/logger.md with accordance to test results and pkg/logger.go
- [ ] Validate logger component documentation against test evidence and source code

### 15. Validate docs/architecture.md with accordance to test results and pkg/*.go
- [ ] Validate architecture documentation against test evidence and all source code

### 16. Make sure README.md is correct and ready
- [ ] Validate README.md accuracy and completeness for release

### 17. Create executive summary document
- [ ] Create work/release_check_$date.md with succinct results (1 line) for each step above

## Completion Criteria

All 17 actions above must be completed with evidence documented before any release.