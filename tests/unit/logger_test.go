package unit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	faro "github.com/T0MASD/faro/pkg"
)

func TestLoggerPrefixes(t *testing.T) {
	// Create temporary directory for test logs
	tmpDir, err := os.MkdirTemp("", "faro-logger-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with debug level to enable all log levels
	config := &faro.Config{
		OutputDir: tmpDir,
		LogLevel:  "debug",
	}

	// Create logger
	logger, err := faro.NewLogger(config)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	// Test component and message
	component := "test-component"
	message := "test message"

	// Log messages at each level
	logger.Debug(component, message)
	logger.Info(component, message)
	logger.Warning(component, message)
	logger.Error(component, message)

	// Give some time for logs to be written
	time.Sleep(100 * time.Millisecond)

	// Find the log file
	logDir := filepath.Join(tmpDir, "logs")
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	var logFile string
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "faro-") && strings.HasSuffix(file.Name(), ".log") {
			logFile = filepath.Join(logDir, file.Name())
			break
		}
	}

	if logFile == "" {
		t.Fatal("No log file found")
	}

	// Read and verify log file contents
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	lines := strings.Split(string(content), "\n")
	
	// Expected prefixes for each log level
	expectedPrefixes := map[string]string{
		"debug":   "D",
		"info":    "I",
		"warning": "W", 
		"error":   "E",
	}

	// Track which log levels we've found
	foundLevels := make(map[string]bool)

	// Check each line for correct prefixes
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Check if line contains our test message
		if !strings.Contains(line, fmt.Sprintf("[%s] %s", component, message)) {
			continue
		}

		// Determine which log level this line represents based on prefix
		for level, expectedPrefix := range expectedPrefixes {
			if strings.HasPrefix(line, expectedPrefix) {
				foundLevels[level] = true
				t.Logf("✓ Found %s log with correct prefix '%s': %s", level, expectedPrefix, line)
				break
			}
		}
	}

	// Verify we found all expected log levels
	for level, expectedPrefix := range expectedPrefixes {
		if !foundLevels[level] {
			t.Errorf("❌ Did not find %s log with prefix '%s'", level, expectedPrefix)
		}
	}

	// Ensure we found at least some log entries
	if len(foundLevels) == 0 {
		t.Error("❌ No log entries with expected prefixes found")
		t.Logf("Log file content:\n%s", string(content))
	}
}

func TestDebugLogPrefix(t *testing.T) {
	// Create temporary directory for test logs
	tmpDir, err := os.MkdirTemp("", "faro-debug-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with debug level
	config := &faro.Config{
		OutputDir: tmpDir,
		LogLevel:  "debug",
	}

	// Create logger
	logger, err := faro.NewLogger(config)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	// Log debug message
	component := "debug-test"
	message := "debug test message"
	logger.Debug(component, message)

	// Give some time for logs to be written
	time.Sleep(100 * time.Millisecond)

	// Find and read the log file
	logDir := filepath.Join(tmpDir, "logs")
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	var logFile string
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "faro-") && strings.HasSuffix(file.Name(), ".log") {
			logFile = filepath.Join(logDir, file.Name())
			break
		}
	}

	if logFile == "" {
		t.Fatal("No log file found")
	}

	// Read log file
	file, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("Failed to open log file: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	debugFound := false

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, fmt.Sprintf("[%s] %s", component, message)) {
			if strings.HasPrefix(line, "D") {
				debugFound = true
				t.Logf("✓ Debug log found with correct 'D' prefix: %s", line)
			} else {
				t.Errorf("❌ Debug log found but with incorrect prefix: %s", line)
			}
			break
		}
	}

	if !debugFound {
		t.Error("❌ Debug log message not found in log file")
	}
}

func TestInfoLogPrefix(t *testing.T) {
	// Create temporary directory for test logs
	tmpDir, err := os.MkdirTemp("", "faro-info-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with info level
	config := &faro.Config{
		OutputDir: tmpDir,
		LogLevel:  "info",
	}

	// Create logger
	logger, err := faro.NewLogger(config)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	// Log info message
	component := "info-test"
	message := "info test message"
	logger.Info(component, message)

	// Give some time for logs to be written
	time.Sleep(100 * time.Millisecond)

	// Find and read the log file
	logDir := filepath.Join(tmpDir, "logs")
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	var logFile string
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "faro-") && strings.HasSuffix(file.Name(), ".log") {
			logFile = filepath.Join(logDir, file.Name())
			break
		}
	}

	if logFile == "" {
		t.Fatal("No log file found")
	}

	// Read log file
	file, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("Failed to open log file: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	infoFound := false

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, fmt.Sprintf("[%s] %s", component, message)) {
			if strings.HasPrefix(line, "I") {
				infoFound = true
				t.Logf("✓ Info log found with correct 'I' prefix: %s", line)
			} else {
				t.Errorf("❌ Info log found but with incorrect prefix: %s", line)
			}
			break
		}
	}

	if !infoFound {
		t.Error("❌ Info log message not found in log file")
	}
}

func TestWarningLogPrefix(t *testing.T) {
	// Create temporary directory for test logs
	tmpDir, err := os.MkdirTemp("", "faro-warning-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with warning level
	config := &faro.Config{
		OutputDir: tmpDir,
		LogLevel:  "warning",
	}

	// Create logger
	logger, err := faro.NewLogger(config)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	// Log warning message
	component := "warning-test"
	message := "warning test message"
	logger.Warning(component, message)

	// Give some time for logs to be written
	time.Sleep(100 * time.Millisecond)

	// Find and read the log file
	logDir := filepath.Join(tmpDir, "logs")
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	var logFile string
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "faro-") && strings.HasSuffix(file.Name(), ".log") {
			logFile = filepath.Join(logDir, file.Name())
			break
		}
	}

	if logFile == "" {
		t.Fatal("No log file found")
	}

	// Read log file
	file, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("Failed to open log file: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	warningFound := false

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, fmt.Sprintf("[%s] %s", component, message)) {
			if strings.HasPrefix(line, "W") {
				warningFound = true
				t.Logf("✓ Warning log found with correct 'W' prefix: %s", line)
			} else {
				t.Errorf("❌ Warning log found but with incorrect prefix: %s", line)
			}
			break
		}
	}

	if !warningFound {
		t.Error("❌ Warning log message not found in log file")
	}
}

func TestErrorLogPrefix(t *testing.T) {
	// Create temporary directory for test logs
	tmpDir, err := os.MkdirTemp("", "faro-error-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with error level
	config := &faro.Config{
		OutputDir: tmpDir,
		LogLevel:  "error",
	}

	// Create logger
	logger, err := faro.NewLogger(config)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	// Log error message
	component := "error-test"
	message := "error test message"
	logger.Error(component, message)

	// Give some time for logs to be written
	time.Sleep(100 * time.Millisecond)

	// Find and read the log file
	logDir := filepath.Join(tmpDir, "logs")
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	var logFile string
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "faro-") && strings.HasSuffix(file.Name(), ".log") {
			logFile = filepath.Join(logDir, file.Name())
			break
		}
	}

	if logFile == "" {
		t.Fatal("No log file found")
	}

	// Read log file
	file, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("Failed to open log file: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	errorFound := false

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, fmt.Sprintf("[%s] %s", component, message)) {
			if strings.HasPrefix(line, "E") {
				errorFound = true
				t.Logf("✓ Error log found with correct 'E' prefix: %s", line)
			} else {
				t.Errorf("❌ Error log found but with incorrect prefix: %s", line)
			}
			break
		}
	}

	if !errorFound {
		t.Error("❌ Error log message not found in log file")
	}
}

func TestJSONExportEnabled(t *testing.T) {
	// Create temporary directory for test logs
	tmpDir, err := os.MkdirTemp("", "faro-json-export-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with JSON export enabled
	config := &faro.Config{
		OutputDir:  tmpDir,
		LogLevel:   "debug",
		JsonExport: true,
	}

	// Create logger
	logger, err := faro.NewLogger(config)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	// Log some messages with valid JSON content from allowed components
	validJSON := `{"eventType":"ADDED","gvr":"v1/pods","namespace":"test","name":"test-pod","uid":"12345"}`
	logger.Debug("controller", validJSON)
	logger.Info("cluster-handler", validJSON)

	// Give some time for logs to be written
	time.Sleep(100 * time.Millisecond)

	// Find the JSON file
	logDir := filepath.Join(tmpDir, "logs")
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	var jsonFile string
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "events-") && strings.HasSuffix(file.Name(), ".json") {
			jsonFile = filepath.Join(logDir, file.Name())
			break
		}
	}

	if jsonFile == "" {
		t.Fatal("❌ No JSON export file found")
	}

	// Verify JSON file exists and has content
	content, err := os.ReadFile(jsonFile)
	if err != nil {
		t.Fatalf("Failed to read JSON file: %v", err)
	}

	if len(content) == 0 {
		t.Fatal("❌ JSON file is empty")
	}

	t.Logf("✓ JSON export file created: %s", jsonFile)
	t.Logf("✓ JSON file content length: %d bytes", len(content))

	// Verify content contains valid JSON lines
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	jsonLinesFound := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Verify each line is valid JSON
		var jsonData interface{}
		if err := json.Unmarshal([]byte(line), &jsonData); err != nil {
			t.Errorf("❌ Invalid JSON line: %s, error: %v", line, err)
		} else {
			jsonLinesFound++
			t.Logf("✓ Valid JSON line found: %s", line)
		}
	}

	if jsonLinesFound == 0 {
		t.Error("❌ No valid JSON lines found in export file")
	}
}

func TestJSONExportDisabled(t *testing.T) {
	// Create temporary directory for test logs
	tmpDir, err := os.MkdirTemp("", "faro-json-disabled-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with JSON export disabled
	config := &faro.Config{
		OutputDir:  tmpDir,
		LogLevel:   "debug",
		JsonExport: false, // Explicitly disabled
	}

	// Create logger
	logger, err := faro.NewLogger(config)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	// Log some messages
	validJSON := `{"eventType":"ADDED","gvr":"v1/pods","namespace":"test","name":"test-pod"}`
	logger.Debug("controller", validJSON)
	logger.Info("cluster-handler", validJSON)

	// Give some time for logs to be written
	time.Sleep(100 * time.Millisecond)

	// Verify no JSON file was created
	logDir := filepath.Join(tmpDir, "logs")
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	for _, file := range files {
		if strings.HasPrefix(file.Name(), "events-") && strings.HasSuffix(file.Name(), ".json") {
			t.Errorf("❌ JSON export file found when it should be disabled: %s", file.Name())
		}
	}

	t.Log("✓ No JSON export file created when JsonExport is disabled")
}

func TestJSONComponentFiltering(t *testing.T) {
	// Create temporary directory for test logs
	tmpDir, err := os.MkdirTemp("", "faro-json-filtering-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with JSON export enabled
	config := &faro.Config{
		OutputDir:  tmpDir,
		LogLevel:   "debug",
		JsonExport: true,
	}

	// Create logger
	logger, err := faro.NewLogger(config)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	validJSON := `{"eventType":"ADDED","gvr":"v1/pods","namespace":"test","name":"test-pod"}`

	// Log messages from allowed components (should appear in JSON file)
	logger.Debug("controller", validJSON)
	logger.Info("cluster-handler", validJSON)

	// Log messages from disallowed components (should NOT appear in JSON file)
	logger.Debug("other-component", validJSON)
	logger.Info("test-component", validJSON)
	logger.Warning("random-component", validJSON)

	// Give some time for logs to be written
	time.Sleep(100 * time.Millisecond)

	// Find and read the JSON file
	logDir := filepath.Join(tmpDir, "logs")
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	var jsonFile string
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "events-") && strings.HasSuffix(file.Name(), ".json") {
			jsonFile = filepath.Join(logDir, file.Name())
			break
		}
	}

	if jsonFile == "" {
		t.Fatal("❌ No JSON export file found")
	}

	// Read and verify JSON file content
	content, err := os.ReadFile(jsonFile)
	if err != nil {
		t.Fatalf("Failed to read JSON file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	validJSONLines := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Verify each line is valid JSON
		var jsonData interface{}
		if err := json.Unmarshal([]byte(line), &jsonData); err == nil {
			validJSONLines++
		}
	}

	// Should only have 2 JSON lines (from controller and cluster-handler components)
	expectedLines := 2
	if validJSONLines != expectedLines {
		t.Errorf("❌ Expected %d JSON lines from allowed components, got %d", expectedLines, validJSONLines)
	} else {
		t.Logf("✓ Correct number of JSON lines found: %d (only from allowed components)", validJSONLines)
	}
}

func TestJSONInvalidContentFiltering(t *testing.T) {
	// Create temporary directory for test logs
	tmpDir, err := os.MkdirTemp("", "faro-json-invalid-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with JSON export enabled
	config := &faro.Config{
		OutputDir:  tmpDir,
		LogLevel:   "debug",
		JsonExport: true,
	}

	// Create logger
	logger, err := faro.NewLogger(config)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	// Log valid JSON (should appear in JSON file)
	validJSON := `{"eventType":"ADDED","gvr":"v1/pods","namespace":"test","name":"test-pod"}`
	logger.Debug("controller", validJSON)

	// Log invalid JSON/plain text (should NOT appear in JSON file)
	logger.Info("controller", "This is not JSON")
	logger.Debug("cluster-handler", "Plain text message")
	logger.Warning("controller", "Another non-JSON message")

	// Give some time for logs to be written
	time.Sleep(100 * time.Millisecond)

	// Find and read the JSON file
	logDir := filepath.Join(tmpDir, "logs")
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	var jsonFile string
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "events-") && strings.HasSuffix(file.Name(), ".json") {
			jsonFile = filepath.Join(logDir, file.Name())
			break
		}
	}

	if jsonFile == "" {
		t.Fatal("❌ No JSON export file found")
	}

	// Read and verify JSON file content
	content, err := os.ReadFile(jsonFile)
	if err != nil {
		t.Fatalf("Failed to read JSON file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	validJSONLines := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Verify each line is valid JSON
		var jsonData interface{}
		if err := json.Unmarshal([]byte(line), &jsonData); err == nil {
			validJSONLines++
			t.Logf("✓ Valid JSON line: %s", line)
		} else {
			t.Errorf("❌ Invalid JSON line found in export file: %s", line)
		}
	}

	// Should only have 1 JSON line (the valid JSON message)
	expectedLines := 1
	if validJSONLines != expectedLines {
		t.Errorf("❌ Expected %d valid JSON lines, got %d", expectedLines, validJSONLines)
	} else {
		t.Logf("✓ Correct filtering: only %d valid JSON line found", validJSONLines)
	}
}

func TestJSONFilePathOutput(t *testing.T) {
	// Create temporary directory for test logs
	tmpDir, err := os.MkdirTemp("", "faro-json-path-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with JSON export enabled
	config := &faro.Config{
		OutputDir:  tmpDir,
		LogLevel:   "info",
		JsonExport: true,
	}

	// Capture stdout to verify the JSON file path is printed
	// Note: In a real test environment, you might want to capture stdout
	// For now, we'll just verify the file is created with the expected naming pattern

	// Create logger
	logger, err := faro.NewLogger(config)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Shutdown()

	// Give some time for initialization
	time.Sleep(50 * time.Millisecond)

	// Verify JSON file was created with correct naming pattern
	logDir := filepath.Join(tmpDir, "logs")
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	jsonFileFound := false
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "events-") && strings.HasSuffix(file.Name(), ".json") {
			jsonFileFound = true
			// Verify the timestamp format in filename (YYYYMMDD-HHMMSS)
			name := file.Name()
			timestampPart := strings.TrimPrefix(name, "events-")
			timestampPart = strings.TrimSuffix(timestampPart, ".json")
			
			if len(timestampPart) != 15 { // YYYYMMDD-HHMMSS = 15 characters
				t.Errorf("❌ JSON file has incorrect timestamp format: %s", name)
			} else {
				t.Logf("✓ JSON file created with correct naming pattern: %s", name)
			}
			break
		}
	}

	if !jsonFileFound {
		t.Error("❌ JSON export file not found with expected naming pattern")
	}
}