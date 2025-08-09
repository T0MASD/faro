package faro

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// LogMessage represents a log message sent through channels
type LogMessage struct {
	Level     int    // klog levels: 0=Info, 1=Warning, 2=Error, 3=Fatal
	Component string
	Message   string
	Timestamp time.Time
}

// Logger provides channel-based logging for all components
type Logger struct {
	logChan chan LogMessage
	logFile *os.File
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewLogger creates a new channel-based logger that uses klog for console and custom file output
func NewLogger(logDir string) (*Logger, error) {
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

	ctx, cancel := context.WithCancel(context.Background())
	
	logger := &Logger{
		logChan: make(chan LogMessage, 1000), // Buffered channel for performance
		logFile: logFile,
		ctx:     ctx,
		cancel:  cancel,
	}
	
	// Start log processing goroutine
	logger.wg.Add(1)
	go logger.processLogs()
	
	return logger, nil
}

// processLogs handles all log messages from the channel
func (l *Logger) processLogs() {
	defer l.wg.Done()
	
	for {
		select {
		case <-l.ctx.Done():
			// Drain remaining messages
			for {
				select {
				case msg := <-l.logChan:
					l.writeLog(msg)
				default:
					return
				}
			}
		case msg := <-l.logChan:
			l.writeLog(msg)
		}
	}
}

// writeLog writes a log message using klog for console, and to file with same format
func (l *Logger) writeLog(msg LogMessage) {
	// Simple format for both console and file - component provides context, not filename
	logLine := fmt.Sprintf("[%s] %s", msg.Component, msg.Message)
	
	// Write to console via klog (klog handles its own timestamp and includes filename)
	switch msg.Level {
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
	
	// Write to file with same format as console (no filename since component provides context)
	if l.logFile != nil {
		// Format similar to klog but without filename: timestamp + level + pid + message
		timestamp := msg.Timestamp.Format("0102 15:04:05.000000")
		pid := os.Getpid()
		var levelChar string
		switch msg.Level {
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
		fileLogLine := fmt.Sprintf("%s%s %d] %s", levelChar, timestamp, pid, logLine)
		l.logFile.WriteString(fmt.Sprintf("%s\n", fileLogLine))
		l.logFile.Sync() // Ensure immediate write
	}
}

// Log sends a log message through the channel
func (l *Logger) Log(level int, component, message string) {
	msg := LogMessage{
		Level:     level,
		Component: component,
		Message:   message,
		Timestamp: time.Now(),
	}
	
	select {
	case l.logChan <- msg:
		// Message sent successfully
	default:
		// Channel full - use klog directly to avoid blocking
		klog.Errorf("[OVERFLOW] [%s] %s", component, message)
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

// Shutdown gracefully shuts down the logger
func (l *Logger) Shutdown() {
	l.cancel()
	l.wg.Wait()
	close(l.logChan)
	if l.logFile != nil {
		l.logFile.Close()
	}
	klog.Flush()
}