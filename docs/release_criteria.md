# Faro Release Criteria

## Release Validation Actions

### 1. Run make test to execute unit, integration and e2e tests, capture the output
- [ ] Execute full test suite
- [ ] Capture complete test output
- [ ] Verify all tests pass with zero failures

### 2. For each unit, integration and e2e tests read and capture faro*.log and events*.json
- [ ] Parse test stdout to identify FARO_LOG_FILE and FARO_JSON_FILE paths for each test
- [ ] Read and capture all faro*.log content from unit tests
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

### 4. Analyze git changes and validate documentation accuracy
- [ ] Run `git diff main..HEAD --name-only` to identify all changed files
- [ ] Run `git diff main..HEAD --stat` to get overview of change scope
- [ ] For each changed source file, run `git diff main..HEAD <file>` to analyze complete changes
- [ ] For each changed documentation file, run `git diff main..HEAD <file>` to analyze updates
- [ ] For each changed test file, run `git diff main..HEAD <file>` to understand test modifications
- [ ] Analyze impact of each change on library functionality and user-facing behavior
- [ ] Identify breaking changes that require version bump or migration guide
- [ ] Validate that documentation changes accurately reflect code changes
- [ ] Verify that configuration field renames are consistently applied across all files
- [ ] Confirm that new features are properly documented with examples
- [ ] Generate comprehensive commit message describing all changes and their impact
- [ ] Determine appropriate version tag based on breaking changes and new features

### 5. Validate server-side filtering implementation
- [ ] Verify FieldSelector is applied for name_selector filtering in controller logs
- [ ] Verify LabelSelector is applied for label-based filtering in controller logs
- [ ] Confirm no client-side filtering occurs in Faro core from test evidence

### 6. Validate readiness callback mechanism
- [ ] Verify SetReadyCallback function works correctly from integration test logs
- [ ] Verify IsReady() returns correct state from test evidence
- [ ] Confirm callback is triggered after controller initialization from logs

### 7. Validate JSON export functionality
- [ ] Verify JSON events contain all required fields (timestamp, action, resource details)
- [ ] Verify JSON format is valid and parseable from events*.json files
- [ ] Confirm JSON export can be enabled/disabled via configuration

### 8. Validate graceful shutdown behavior
- [ ] Verify controller stops cleanly when context is cancelled from logs
- [ ] Verify all goroutines terminate properly during shutdown from logs
- [ ] Confirm no resource leaks during shutdown process

### 9. Validate configuration normalization
- [ ] Verify config.Normalize() handles all resource types correctly from test logs
- [ ] Verify namespace-centric vs resource-centric configurations work from test evidence
- [ ] Confirm invalid configurations are rejected with proper error messages

### 10. Validate Kubernetes client integration
- [ ] Verify dynamic client connects to all required API groups from logs
- [ ] Verify informers are created for all configured resources from logs
- [ ] Confirm client handles API server connection issues gracefully

### 11. Validate event processing pipeline
- [ ] Verify events are captured for CREATE, UPDATE, DELETE operations from logs
- [ ] Verify event timestamps are accurate and consistent from JSON exports
- [ ] Confirm event processing doesn't drop or duplicate events

### 12. Validate docs/components/controller.md with accordance to test results and pkg/controller.go
- [ ] Validate controller component documentation based on test evidence and source code

### 13. Validate docs/components/client.md with accordance to test results and pkg/client.go
- [ ] Validate client component documentation against test evidence and source code

### 14. Validate docs/components/config.md with accordance to test results and pkg/config.go
- [ ] Validate config component documentation against test evidence and source code

### 15. Validate docs/components/logger.md with accordance to test results and pkg/logger.go
- [ ] Validate logger component documentation against test evidence and source code

### 16. Validate docs/architecture.md with accordance to test results and pkg/*.go
- [ ] Validate architecture documentation against test evidence and all source code

### 17. Verify README.md is correct and complete
- [ ] Validate README.md accuracy and completeness for release

### 18. Create executive summary document
- [ ] Create work/release_check_$date.md with succinct results (1 line) for each step above

## Completion Criteria

All 18 validation steps above must be completed with evidence documented before any release.

**Note**: Step 4 (git diff analysis) is critical for validating that documentation accurately reflects code changes and should be executed when validating existing documentation against current implementation.