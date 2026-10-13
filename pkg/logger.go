package faro

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sync"
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

	// Default json_rotate_interval. An hour caps a capture file at ~3600
	// fsyncs, and so ~3600 chunks on a chunked filesystem, which measured at
	// about 80ms per sync - well inside the one-second interval. See
	// Config.JsonRotateInterval for the curve this is derived from.
	captureRotateInterval = time.Hour
)

// captureItem is one thing for the writer to do, and both kinds travel on the
// same channel ON PURPOSE.
//
// A flush request on a channel of its own is serviced out of order: select
// picks uniformly among ready cases, so the writer could answer a flush while
// records were still queued, flush an empty buffer, and acknowledge a flush
// that had written nothing. One channel makes a flush mean "everything
// enqueued before me is on disk", which is the only useful meaning.
type captureItem struct {
	line string
	ack  chan struct{} // non-nil: flush what is buffered, then close this
}

// sinkOpener starts the NEXT capture file. Rotation is the writer's, so it
// happens between records: nothing is lost or duplicated across the boundary,
// and FlushCapture keeps meaning "everything enqueued before me is on disk".
type sinkOpener func() (captureSink, string, error)

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
	lines      chan captureItem
	writerDone chan struct{}
	syncEvery  bool // json_sync: "record"

	// Rotation. openNext is nil when the capture cannot be rotated - a Logger
	// built around a fixed sink, as the tests do - and the thresholds are then
	// ignored rather than silently failing to act on them.
	openNext      sinkOpener
	rotateRecords int
	rotateEvery   time.Duration
}

// startCaptureWriter wires up the writer. Split out of NewLogger so a test can
// build a Logger around an arbitrary sink.
func (l *Logger) startCaptureWriter(sink captureSink, syncEvery bool) {
	l.syncEvery = syncEvery
	l.lines = make(chan captureItem, captureQueueSize)
	l.writerDone = make(chan struct{})
	go l.runCaptureWriter(sink, l.lines)
}

// FlushCapture writes everything queued to the file, fsyncs it, and waits for
// that to finish.
//
// Writes are asynchronous and, by default, synced once a second, so anything
// that reads the capture back - a test, a tool, an operator about to copy the
// file - must ask for a flush first rather than sleeping and hoping.
func (l *Logger) FlushCapture() {
	ack := make(chan struct{})
	l.mu.RLock()
	if l.lines == nil {
		l.mu.RUnlock()
		return // already shut down, and shutdown flushes
	}
	l.lines <- captureItem{ack: ack}
	done := l.writerDone
	l.mu.RUnlock()
	select {
	case <-ack:
	case <-done:
		// The writer stopped before reaching it; shutting down flushes anyway.
	}
}

// The channel is passed in rather than read from l.lines each iteration:
// Shutdown nils that field before closing the channel, so a writer reading
// the field would end up selecting on a nil channel and block forever - and
// reading it unlocked would be a data race besides.
func (l *Logger) runCaptureWriter(sink captureSink, lines <-chan captureItem) {
	defer close(l.writerDone)
	bw := bufio.NewWriterSize(sink, 64*1024)
	t := time.NewTicker(captureSyncInterval)
	defer t.Stop()

	// Nothing written since the last sync means there is nothing to sync.
	//
	// The ticker fires on wall-clock time, not on activity, so without this a
	// collector watching a quiet cluster still fsyncs once a second forever.
	// That is not free: on a chunked network filesystem EVERY fsync appends a
	// chunk to the file, and fsync latency grows with the number of chunks in
	// it. Measured on a SeaweedFS CSI mount, 2000 syncs of 300 bytes each: the
	// median went 14ms -> 44ms, monotonically, while the same test on an
	// emptyDir stayed flat at 11-14ms. So an idle collector was buying itself
	// a slower capture in exchange for nothing at all.
	//
	// This makes chunk growth proportional to what was actually written rather
	// than to how long the process has been alive. It does not bound it - see
	// the rotation issue for that - it stops paying for silence.
	dirty := false
	flush := func() {
		if !dirty {
			return
		}
		dirty = false
		if err := bw.Flush(); err != nil {
			klog.Errorf("[logger] JSON capture flush failed, these events are not in the capture: %v", err)
			return
		}
		if err := sink.Sync(); err != nil {
			klog.Errorf("[logger] JSON capture sync failed, these events may not be in the capture: %v", err)
		}
	}

	// Rotation state. Here rather than on the Logger because only this
	// goroutine may touch it.
	written := 0
	lastRotate := time.Now()
	rotate := func(why string) {
		flush()
		if c, ok := sink.(io.Closer); ok {
			_ = c.Close()
		}
		next, name, err := l.openNext()
		if err != nil {
			// Keep writing to the file we have. A failed rotation must not end
			// the capture: a file that keeps growing is bad, no file is worse.
			klog.Errorf("[logger] could not start a new capture file, staying on the current one: %v", err)
			written, lastRotate = 0, time.Now()
			return
		}
		sink = next
		bw.Reset(sink)
		written, lastRotate = 0, time.Now()
		klog.Infof("[logger] capture rotated (%s) -> %s", why, name)
	}
	due := func() string {
		if l.openNext == nil || written == 0 {
			return ""
		}
		if l.rotateRecords > 0 && written >= l.rotateRecords {
			return fmt.Sprintf("%d records", written)
		}
		if l.rotateEvery > 0 && time.Since(lastRotate) >= l.rotateEvery {
			return "interval"
		}
		return ""
	}

	for {
		select {
		case item, ok := <-lines:
			if !ok {
				flush()
				if c, ok := sink.(io.Closer); ok {
					_ = c.Close()
				}
				return
			}
			if item.ack != nil {
				flush()
				close(item.ack)
				continue
			}
			if _, err := bw.WriteString(item.line + "\n"); err != nil {
				klog.Errorf("[logger] JSON capture write failed, this event is not in the capture: %v", err)
				continue
			}
			dirty = true
			written++
			if l.syncEvery {
				flush()
			}
			if why := due(); why != "" {
				rotate(why)
			}
		case <-t.C:
			flush()
			// due() returns "" when nothing has been written since the last
			// rotation, so an idle collector does not leave a trail of empty
			// files: with no writes there are no chunks to escape.
			if why := due(); why != "" {
				rotate(why)
			}
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
			// Each rotation opens a file named the way the first one is, so a
			// reader concatenating events-*.json in name order gets them in
			// order - which is what readers already do across a pod restart.
			// The seconds-resolution timestamp can repeat, so a collision gets
			// a suffix rather than overwriting a capture.
			logger.openNext = func() (captureSink, string, error) {
				base := fmt.Sprintf("%s/events-%s", logDir, time.Now().Format("20060102-150405"))
				path := base + ".json"
				for n := 2; ; n++ {
					if _, err := os.Stat(path); os.IsNotExist(err) {
						break
					}
					path = fmt.Sprintf("%s-%d.json", base, n)
				}
				f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
				if err != nil {
					return nil, "", err
				}
				return f, path, nil
			}
			logger.rotateRecords = config.JsonRotateRecords
			logger.rotateEvery = captureRotateInterval
			if config.JsonRotateInterval != "" {
				d, err := time.ParseDuration(config.JsonRotateInterval)
				switch {
				case err != nil:
					klog.Errorf("[logger] json_rotate_interval %q is not a duration, using %v: %v",
						config.JsonRotateInterval, captureRotateInterval, err)
				case d < 0:
					klog.Errorf("[logger] json_rotate_interval %q is negative, using %v",
						config.JsonRotateInterval, captureRotateInterval)
				default:
					logger.rotateEvery = d // 0 disables it
				}
			}
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
	// up as a producer that waits; dropping would be silent loss, which is the
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
	l.lines <- captureItem{line: message}
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
