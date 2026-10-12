package faro

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/klog/v2"
)

// How often the capture is flushed and fsynced when json_sync is "interval",
// and how many encoded records may wait for the writer.
//
// The queue is bounded on purpose. Unbounded, excess load becomes unbounded
// memory and a lag nobody can see; bounded, a producer that outruns the disk
// blocks, which is backpressure and is measurable as a full queue. Dropping
// instead was never an option - a capture that silently discards records is
// the failure this whole file keeps being fixed for.
const (
	captureSyncInterval = time.Second
	captureQueueSize    = 4096
)

// captureSink is what the capture writer needs of its output. *os.File
// satisfies it; a test can substitute something with a slow Sync, which is the
// condition this whole mechanism exists for.
type captureSink interface {
	io.Writer
	Sync() error
}

var klogInitOnce sync.Once

// Logger provides logging using klog directly
type Logger struct {
	jsonFile  *os.File
	logWriter io.Writer // Writer for log output (stderr + file)
	mu        sync.RWMutex

	// The capture is written by one goroutine reading this channel, so the
	// callers of LogJSON are never serialized on disk latency. Before this,
	// LogJSON held l.mu across WriteString AND Sync, so all three workers
	// queued behind one fsync and throughput was 1/fsync-latency regardless of
	// how many of them ran (#35).
	lines      chan string
	writerDone chan struct{}
	syncEvery  bool // json_sync: "record"

	written  atomic.Uint64 // records handed to the file
	failed   atomic.Uint64 // records that could not be written
	syncFail atomic.Uint64 // flush/sync errors
}

// CaptureStats is what the capture writer can say about itself.
//
// Queued is the part worth watching: it is the backlog that used to be
// invisible, and a sustained non-zero value means the output volume cannot
// keep up with the event rate.
type CaptureStats struct {
	Written      uint64
	Failed       uint64
	SyncFailures uint64
	Queued       int
	QueueSize    int
}

// CaptureStats reports the capture writer's counters.
func (l *Logger) CaptureStats() CaptureStats {
	l.mu.RLock()
	ch := l.lines
	l.mu.RUnlock()
	return CaptureStats{
		Written:      l.written.Load(),
		Failed:       l.failed.Load(),
		SyncFailures: l.syncFail.Load(),
		Queued:       len(ch),
		QueueSize:    cap(ch),
	}
}

// runCaptureWriter owns jsonFile for the lifetime of the logger: nothing else
// touches it until Shutdown has waited for this to return, so it needs no lock.
// startCaptureWriter wires up the writer. Split out of NewLogger so a test can
// build a Logger around an arbitrary sink.
func (l *Logger) startCaptureWriter(sink captureSink, syncEvery bool) {
	l.syncEvery = syncEvery
	l.lines = make(chan string, captureQueueSize)
	l.writerDone = make(chan struct{})
	go l.runCaptureWriter(sink, l.lines)
}

// The channel is passed in rather than read from l.lines each iteration:
// Shutdown nils that field before closing the channel, so a writer reading
// the field would end up selecting on a nil channel and block forever - and
// reading it unlocked would be a data race besides.
func (l *Logger) runCaptureWriter(sink captureSink, lines <-chan string) {
	defer close(l.writerDone)
	bw := bufio.NewWriterSize(sink, 64*1024)
	t := time.NewTicker(captureSyncInterval)
	defer t.Stop()

	// Say so when a write fails. Both calls return an error and both used to be
	// discarded, so the capture could stop at an arbitrary point while the
	// process stayed healthy and the text log carried on - with nothing logged
	// at any level, and afterwards no way to say why.
	flush := func() {
		if err := bw.Flush(); err != nil {
			l.syncFail.Add(1)
			klog.Errorf("[logger] JSON capture flush failed, these events are not in the capture: %v", err)
			return
		}
		if err := sink.Sync(); err != nil {
			l.syncFail.Add(1)
			klog.Errorf("[logger] JSON capture sync failed, these events may not be in the capture: %v", err)
		}
	}

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				flush()
				return
			}
			if _, err := bw.WriteString(line + "\n"); err != nil {
				l.failed.Add(1)
				klog.Errorf("[logger] JSON capture write failed, this event is not in the capture: %v", err)
				continue
			}
			l.written.Add(1)
			if l.syncEvery {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
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

	// Parse flags to make the verbosity setting take effect
	flag.Parse()

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

		// Create log file
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to create log file: %v", err)
		}

		// Create a MultiWriter to write to both stderr and file
		multiWriter := io.MultiWriter(os.Stderr, logFile)
		logger.logWriter = multiWriter // Store for debug method

		// Configure klog to write to both stderr and file
		flag.Set("logtostderr", "false") // Don't log to stderr by default
		klog.SetOutput(multiWriter)      // Log to both stderr and file

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
			logger.startCaptureWriter(jsonFile, config.JsonSync == "record")

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

	// Hand the line to the writer instead of writing it here.
	//
	// This used to take l.mu and hold it across WriteString AND Sync, so every
	// worker queued behind one fsync: throughput was 1/fsync-latency no matter
	// how many workers ran, which on a network filesystem is ~8 records/s (#35).
	// The lock is still taken, but only to read the channel - never across I/O.
	//
	// A full queue BLOCKS rather than drops. That is backpressure, and it shows
	// up in CaptureStats().Queued; dropping would be silent loss, which is the
	// failure this function keeps being fixed for.
	// The read lock is held ACROSS the send, not just to fetch the channel.
	// Shutdown closes it under the write lock, so holding this makes a send
	// racing that close impossible rather than something to recover from. A
	// send that blocks on a full queue does hold the lock, and Shutdown then
	// waits for it - which is correct: the writer is still draining, so the
	// send completes.
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.lines == nil {
		return
	}
	l.lines <- message
}

// Debug logs a debug message with proper D level formatting
func (l *Logger) Debug(component, message string) {
	logLine := fmt.Sprintf("[%s] %s", component, message)

	// Only show debug messages if verbosity is enabled
	if klog.V(1).Enabled() {
		// Manually format as debug message with D prefix instead of I
		timestamp := time.Now().Format("0102 15:04:05.000000")
		pid := os.Getpid()
		if l.logWriter != nil {
			fmt.Fprintf(l.logWriter, "D%s %7d logger.go:133] %s\n", timestamp, pid, logLine)
		} else {
			fmt.Fprintf(os.Stderr, "D%s %7d logger.go:133] %s\n", timestamp, pid, logLine)
		}
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
	// Stop the writer and WAIT for it before closing the file: it flushes and
	// syncs whatever is still queued on its way out, and closing underneath it
	// would discard exactly the records a shutdown most needs to keep.
	l.mu.Lock()
	ch, done := l.lines, l.writerDone
	l.lines = nil
	if ch != nil {
		close(ch)
	}
	l.mu.Unlock()
	if ch != nil {
		<-done
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Close JSON file if open
	if l.jsonFile != nil {
		l.jsonFile.Close()
		l.jsonFile = nil
	}

	klog.Flush()
}
