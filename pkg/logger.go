package faro

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

var klogInitOnce sync.Once

// Logger provides logging using klog directly
type Logger struct {
	jsonFile *os.File
	mu       sync.RWMutex
}

// NewLogger creates a logger that uses klog directly
func NewLogger(config *Config) (*Logger, error) {
	logger := &Logger{}
	
	// Initialize klog flags only once globally
	klogInitOnce.Do(func() {
		klog.InitFlags(nil)
	})
	
	// Configure klog verbosity based on log level
	// This ensures debug messages are only shown when log level is debug
	if config.LogLevel == "debug" {
		flag.Set("v", "1") // Enable klog verbosity level 1 for debug messages
	} else {
		flag.Set("v", "0") // Disable debug verbosity for non-debug levels
	}
	
	// Set up file output if specified
	logDir := config.GetLogDir()
	if logDir != "" {
		// Ensure log directory exists
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create log directory: %v", err)
		}

		// Create log file with timestamp
		timestamp := time.Now().Format("20060102-150405")
		logPath := fmt.Sprintf("%s/faro-%s.log", logDir, timestamp)
		
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to create log file: %v", err)
		}
		
		// Set klog to write to our file
		klog.SetOutput(logFile)
		
		// Log file path to stdout for test identification
		fmt.Printf("FARO_LOG_FILE: %s\n", logPath)
		
		// Handle JSON export separately if requested
		if config.JsonExport {
			jsonPath := fmt.Sprintf("%s/events-%s.json", logDir, timestamp)
			jsonFile, err := os.OpenFile(jsonPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				return nil, fmt.Errorf("failed to create JSON log file: %v", err)
			}
			
			logger.jsonFile = jsonFile
			
			// Log JSON file path to stdout for test identification
			fmt.Printf("FARO_JSON_FILE: %s\n", jsonPath)
		}
	}
	
	return logger, nil
}

// SetConsoleEnabled enables or disables console output
func (l *Logger) SetConsoleEnabled(enabled bool) {
	// For klog, we can redirect to /dev/null to disable console
	if !enabled {
		klog.SetOutput(os.NewFile(0, os.DevNull))
	} else {
		klog.SetOutput(os.Stderr)
	}
}

// LogJSON writes JSON events to the JSON file if configured
func (l *Logger) LogJSON(component, message string) {
	// Only handle messages from components that generate JSON events
	if component != "cluster-handler" && component != "controller" {
		return
	}
	
	// Check if the message is valid JSON
	var jsonData interface{}
	if err := json.Unmarshal([]byte(message), &jsonData); err != nil {
		// Not JSON, skip
		return
	}
	
	if l.jsonFile != nil {
		l.mu.Lock()
		defer l.mu.Unlock()
		
		// Write pure JSON (one line per event)
		l.jsonFile.WriteString(message + "\n")
		l.jsonFile.Sync() // Ensure immediate write
	}
}

// Debug logs a debug message with proper D level formatting
func (l *Logger) Debug(component, message string) {
	logLine := fmt.Sprintf("[%s] %s", component, message)
	
	// Since klog doesn't have native debug level, we need to manually format it
	// Only show debug messages if verbosity is enabled
	if klog.V(1).Enabled() {
		// Format as debug message with D prefix instead of I
		timestamp := time.Now().Format("0102 15:04:05.000000")
		pid := os.Getpid()
		fmt.Fprintf(os.Stderr, "D%s %7d logger.go:117] %s\n", timestamp, pid, logLine)
	}
	
	l.LogJSON(component, message)
}

// Info logs an info message
func (l *Logger) Info(component, message string) {
	logLine := fmt.Sprintf("[%s] %s", component, message)
	klog.Info(logLine)
	l.LogJSON(component, message)
}

// Warning logs a warning message
func (l *Logger) Warning(component, message string) {
	logLine := fmt.Sprintf("[%s] %s", component, message)
	klog.Warning(logLine)
	l.LogJSON(component, message)
}

// Error logs an error message
func (l *Logger) Error(component, message string) {
	logLine := fmt.Sprintf("[%s] %s", component, message)
	klog.Error(logLine)
	l.LogJSON(component, message)
}

// Fatal logs a fatal message
func (l *Logger) Fatal(component, message string) {
	logLine := fmt.Sprintf("[%s] %s", component, message)
	klog.Fatal(logLine)
}

// Shutdown gracefully shuts down the logger
func (l *Logger) Shutdown() {
	l.mu.Lock()
	defer l.mu.Unlock()
	
	// Close JSON file if open
	if l.jsonFile != nil {
		l.jsonFile.Close()
		l.jsonFile = nil
	}
	
	klog.Flush()
}