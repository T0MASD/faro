package faro

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// LogHandler interface for handling log messages via callbacks
type LogHandler interface {
	WriteLog(level int, component, message string, timestamp time.Time) error
	Name() string
	Close() error
}

// Logger provides callback-based logging for all components
type Logger struct {
	handlers []LogHandler
	mu       sync.RWMutex
}

// NewLogger creates a new callback-based logger from config
func NewLogger(config *Config) (*Logger, error) {
	logger := &Logger{
		handlers: make([]LogHandler, 0),
	}
	
	// Add console handler (always present)
	logger.AddHandler(&ConsoleLogHandler{})
	
	// Add file handler if logDir is specified
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
		
		logger.AddHandler(&FileLogHandler{file: logFile})
		
		// Log file path to stdout for test identification
		fmt.Printf("FARO_LOG_FILE: %s\n", logPath)
		
		// Add JSON file handler if requested
		if config.JsonExport {
			jsonPath := fmt.Sprintf("%s/events-%s.json", logDir, timestamp)
			jsonFile, err := os.OpenFile(jsonPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				return nil, fmt.Errorf("failed to create JSON log file: %v", err)
			}
			
			logger.AddHandler(&JSONFileHandler{file: jsonFile})
			
			// Log JSON file path to stdout for test identification
			fmt.Printf("FARO_JSON_FILE: %s\n", jsonPath)
		}
	}
	
	return logger, nil
}

// AddHandler adds a log handler to the logger
func (l *Logger) AddHandler(handler LogHandler) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.handlers = append(l.handlers, handler)
}

// SetConsoleEnabled enables or disables console output
func (l *Logger) SetConsoleEnabled(enabled bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	
	for _, handler := range l.handlers {
		if consoleHandler, ok := handler.(*ConsoleLogHandler); ok {
			consoleHandler.Disabled = !enabled
		}
	}
}

// ConsoleLogHandler handles logging to console via klog
type ConsoleLogHandler struct{
	Disabled bool
}

func (ch *ConsoleLogHandler) Name() string {
	return "console"
}

func (ch *ConsoleLogHandler) WriteLog(level int, component, message string, timestamp time.Time) error {
	// Skip console output if disabled (e.g., when dashboard is active)
	if ch.Disabled {
		return nil
	}
	
	logLine := fmt.Sprintf("[%s] %s", component, message)
	
	switch level {
	case -1: // Debug
		klog.V(1).Info(logLine) // Use klog verbosity level 1 for debug
	case 0: // Info
		klog.Info(logLine)
	case 1: // Warning
		klog.Warning(logLine)
	case 2: // Error
		klog.Error(logLine)
	case 3: // Fatal
		klog.Fatal(logLine)
	default:
		klog.Info(logLine)
	}
	
	return nil
}

func (ch *ConsoleLogHandler) Close() error {
	// Console handler has nothing to close
	return nil
}

// FileLogHandler handles logging to file
type FileLogHandler struct {
	file *os.File
	mu   sync.Mutex
}

func (fh *FileLogHandler) Name() string {
	return "file"
}

func (fh *FileLogHandler) WriteLog(level int, component, message string, timestamp time.Time) error {
	fh.mu.Lock()
	defer fh.mu.Unlock()
	
	if fh.file == nil {
		return fmt.Errorf("file handler is closed")
	}
	
	logLine := fmt.Sprintf("[%s] %s", component, message)
	
	// Format similar to klog but without filename: timestamp + level + pid + message
	timestampStr := timestamp.Format("0102 15:04:05.000000")
	pid := os.Getpid()
	
	var levelChar string
	switch level {
	case -1:
		levelChar = "D"
	case 0:
		levelChar = "I"
	case 1:
		levelChar = "W"
	case 2:
		levelChar = "E"
	case 3:
		levelChar = "F"
	default:
		levelChar = "I"
	}
	
	// Clean format without filename - component provides better context
	fileLogLine := fmt.Sprintf("%s%s %d] %s\n", levelChar, timestampStr, pid, logLine)
	
	if _, err := fh.file.WriteString(fileLogLine); err != nil {
		return err
	}
	
	return fh.file.Sync() // Ensure immediate write
}

func (fh *FileLogHandler) Close() error {
	fh.mu.Lock()
	defer fh.mu.Unlock()
	
	if fh.file != nil {
		err := fh.file.Close()
		fh.file = nil
		return err
	}
	return nil
}

// JSONFileHandler handles logging structured JSON events to a separate file
type JSONFileHandler struct {
	file *os.File
	mu   sync.Mutex
}

func (jh *JSONFileHandler) Name() string {
	return "json-file"
}

func (jh *JSONFileHandler) WriteLog(level int, component, message string, timestamp time.Time) error {
	// Only handle messages from components that generate JSON events
	if component != "cluster-handler" && component != "controller" {
		return nil
	}
	
	// Check if the message is valid JSON
	var jsonData interface{}
	if err := json.Unmarshal([]byte(message), &jsonData); err != nil {
		// Not JSON, skip
		return nil
	}
	
	jh.mu.Lock()
	defer jh.mu.Unlock()
	
	if jh.file == nil {
		return fmt.Errorf("JSON file handler is closed")
	}
	
	// Write pure JSON (one line per event)
	if _, err := jh.file.WriteString(message + "\n"); err != nil {
		return err
	}
	
	return jh.file.Sync() // Ensure immediate write
}

func (jh *JSONFileHandler) Close() error {
	jh.mu.Lock()
	defer jh.mu.Unlock()
	
	if jh.file != nil {
		err := jh.file.Close()
		jh.file = nil
		return err
	}
	return nil
}

// Log calls all registered handlers with the log message
func (l *Logger) Log(level int, component, message string) {
	timestamp := time.Now()
	
	l.mu.RLock()
	handlers := l.handlers
	l.mu.RUnlock()
	
	// Call all handlers directly - no channels or buffering
	for _, handler := range handlers {
		if err := handler.WriteLog(level, component, message, timestamp); err != nil {
			// Fallback to klog only on handler failure to avoid infinite loops
			klog.Errorf("Log handler '%s' failed: %v", handler.Name(), err)
		}
	}
}

// Debug logs a debug message (level -1, only shown with debug log level)
func (l *Logger) Debug(component, message string) {
	l.Log(-1, component, message)
}

// Info logs an info message (level 0)
func (l *Logger) Info(component, message string) {
	l.Log(0, component, message)
}

// Warning logs a warning message (level 1)
func (l *Logger) Warning(component, message string) {
	l.Log(1, component, message)
}

// Error logs an error message (level 2)
func (l *Logger) Error(component, message string) {
	l.Log(2, component, message)
}

// Fatal logs a fatal message (level 3)
func (l *Logger) Fatal(component, message string) {
	l.Log(3, component, message)
}

// Shutdown gracefully shuts down the logger by closing all handlers
func (l *Logger) Shutdown() {
	l.mu.Lock()
	defer l.mu.Unlock()
	
	// Close all handlers
	for _, handler := range l.handlers {
		if err := handler.Close(); err != nil {
			klog.Errorf("Failed to close log handler '%s': %v", handler.Name(), err)
		}
	}
	
	// Clear handlers
	l.handlers = nil
	
	klog.Flush()
}