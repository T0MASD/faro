package faro

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"time"

	"k8s.io/klog/v2"
)

func init() {
	// The file capture. Registered like any other destination, though it is the
	// one with a lifecycle on the Logger: Shutdown closes its handle, and
	// rotation needs an opener that knows where the first file went.
	RegisterSink("file", func(config *Config, l *Logger) (Sink, error) {
		if !config.JsonExport {
			return nil, nil
		}
		logDir := config.GetLogDir()
		if logDir == "" {
			return nil, nil
		}
		timestamp := time.Now().Format("20060102-150405")
		jsonPath := fmt.Sprintf("%s/events-%s.json", logDir, timestamp)
		jsonFile, err := os.OpenFile(jsonPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to create JSON log file: %v", err)
		}
		l.jsonFile = jsonFile

		// Each rotation opens a file named the way the first one is, so a
		// reader concatenating events-*.json in name order gets them in order -
		// which is what readers already do across a pod restart. The
		// seconds-resolution timestamp can repeat, so a collision gets a suffix
		// rather than overwriting a capture.
		l.openNext = func() (captureSink, string, error) {
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
		l.rotateRecords = config.JsonRotateRecords
		l.rotateEvery = captureRotateInterval
		if config.JsonRotateInterval != "" {
			d, perr := time.ParseDuration(config.JsonRotateInterval)
			switch {
			case perr != nil:
				klog.Errorf("[logger] json_rotate_interval %q is not a duration, using %v: %v",
					config.JsonRotateInterval, captureRotateInterval, perr)
			case d < 0:
				klog.Errorf("[logger] json_rotate_interval %q is negative, using %v",
					config.JsonRotateInterval, captureRotateInterval)
			default:
				l.rotateEvery = d // 0 disables it
			}
		}
		l.syncEvery = config.JsonSync == "record"

		fmt.Printf("FARO_JSON_FILE: %s\n", jsonPath)
		return newFileSink(jsonFile, l.syncEvery, l.openNext, l.rotateRecords, l.rotateEvery), nil
	})
}

// fileSink writes the capture as one JSON object per line.
//
// This is the behaviour faro has always had, moved behind the Sink interface
// unchanged: a buffered writer, a sync policy, and rotation. The reasoning for
// each is in the fields below and in Config.
type fileSink struct {
	out       captureSink
	bw        *bufio.Writer
	dirty     bool // something written since the last sync
	syncEvery bool // json_sync: "record"

	openNext      sinkOpener
	rotateRecords int
	rotateEvery   time.Duration
	written       int
	lastRotate    time.Time
}

func newFileSink(out captureSink, syncEvery bool, open sinkOpener, rotateRecords int, rotateEvery time.Duration) *fileSink {
	return &fileSink{
		out:           out,
		bw:            bufio.NewWriterSize(out, 64*1024),
		syncEvery:     syncEvery,
		openNext:      open,
		rotateRecords: rotateRecords,
		rotateEvery:   rotateEvery,
		lastRotate:    time.Now(),
	}
}

func (f *fileSink) Append(e Event) error {
	if _, err := f.bw.Write(e.JSON); err != nil {
		return err
	}
	if err := f.bw.WriteByte('\n'); err != nil {
		return err
	}
	f.dirty = true
	f.written++
	if f.syncEvery {
		if err := f.Flush(); err != nil {
			return err
		}
	}
	f.rotateIfDue()
	return nil
}

// Flush does nothing when nothing has been written.
//
// The sync ticker fires on wall-clock time, not on activity, so without this a
// collector watching a quiet cluster fsyncs once a second forever. On a chunked
// network filesystem every fsync appends a chunk and fsync latency grows with
// the chunk count, so an idle collector was making its own capture slower in
// exchange for nothing.
func (f *fileSink) Flush() error {
	if !f.dirty {
		return nil
	}
	f.dirty = false
	if err := f.bw.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	if err := f.out.Sync(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	return nil
}

func (f *fileSink) Close() error {
	err := f.Flush()
	if c, ok := f.out.(io.Closer); ok {
		_ = c.Close()
	}
	return err
}

// Tick gives the sink the periodic wakeup its policies need. A sink with no
// time-based behaviour can ignore it; this one syncs and may rotate.
func (f *fileSink) Tick() error {
	err := f.Flush()
	f.rotateIfDue()
	return err
}

func (f *fileSink) rotateIfDue() {
	why := ""
	switch {
	case f.openNext == nil || f.written == 0:
		return
	case f.rotateRecords > 0 && f.written >= f.rotateRecords:
		why = fmt.Sprintf("%d records", f.written)
	case f.rotateEvery > 0 && time.Since(f.lastRotate) >= f.rotateEvery:
		why = "interval"
	default:
		return
	}

	if err := f.Flush(); err != nil {
		klog.Errorf("[logger] JSON capture flush failed, these events are not in the capture: %v", err)
	}
	if c, ok := f.out.(io.Closer); ok {
		_ = c.Close()
	}
	next, name, err := f.openNext()
	if err != nil {
		// Keep writing to the file we have. A failed rotation must not end the
		// capture: a file that keeps growing is bad, no file is worse.
		klog.Errorf("[logger] could not start a new capture file, staying on the current one: %v", err)
		f.written, f.lastRotate = 0, time.Now()
		return
	}
	f.out = next
	f.bw.Reset(f.out)
	f.written, f.lastRotate = 0, time.Now()
	klog.Infof("[logger] capture rotated (%s) -> %s", why, name)
}
