package faro

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"k8s.io/klog/v2"
)

const okEvent = `{"eventType":"ADDED","gvr":"v1/pods","name":"p1"}`

func newJSONLogger(t *testing.T) (*Logger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	l := &Logger{jsonFile: f}
	// Per-record sync, so a write error surfaces on the record that caused it
	// rather than on the next tick - which is what these tests are about.
	l.startCaptureWriter(f, true)
	return l, path
}

// Events still reach the file.
func TestLogJSONWritesEvents(t *testing.T) {
	l, path := newJSONLogger(t)
	for i := 0; i < 3; i++ {
		l.LogJSON("controller", okEvent)
	}
	// The capture is written by its own goroutine now, so read it back only
	// after Shutdown has drained and synced it.
	l.Shutdown()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "\n"); n != 3 {
		t.Fatalf("wrote %d lines, want 3", n)
	}
}

// THE REGRESSION TEST. A failed write must say so.
//
// Against the unfixed code this fails with "produced no report": the error was
// discarded, so the capture stopped at an arbitrary point with nothing logged
// at any level. That silence is the bug - it is why a 27-minute gap observed in
// production could not be explained afterwards.
//
// The file is closed underneath the logger so the write fails for real rather
// than being mocked.
func TestLogJSONWriteFailureIsReported(t *testing.T) {
	l, _ := newJSONLogger(t)

	var buf syncBuffer
	// klog defaults to logtostderr=true, which bypasses SetOutput entirely - the
	// line goes to stderr and the buffer stays empty, so the test would pass
	// while proving nothing.
	klogInitOnce.Do(func() { klog.InitFlags(nil) })
	flag.Set("logtostderr", "false")
	klog.SetOutput(&buf)
	t.Cleanup(func() {
		klog.Flush()
		flag.Set("logtostderr", "true")
		klog.SetOutput(os.Stderr)
	})

	l.mu.Lock()
	_ = l.jsonFile.Close()
	l.mu.Unlock()

	l.LogJSON("controller", okEvent)
	// Drain the writer: the record is handed over asynchronously, and it is the
	// writer that discovers the file is gone.
	l.Shutdown()
	klog.Flush()

	if got := buf.String(); !strings.Contains(got, "JSON capture") {
		t.Fatalf("a failed JSON write produced no report.\nklog output was:\n%s", got)
	}
}

// LogJSON and Shutdown touch jsonFile from different goroutines. Before the
// fix the nil-check sat outside the lock Shutdown holds; this fails under
// -race without it.
func TestLogJSONConcurrentWithShutdown(t *testing.T) {
	l, _ := newJSONLogger(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				l.LogJSON("controller", fmt.Sprintf(`{"eventType":"ADDED","name":"p%d-%d"}`, n, j))
			}
		}(i)
	}
	wg.Add(1)
	go func() { defer wg.Done(); l.Shutdown() }()
	wg.Wait()
}

// klog writes from its own goroutines; guard the buffer.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
