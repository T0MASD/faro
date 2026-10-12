package faro

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// A sink whose Sync is slow, which is the condition this mechanism exists for:
// the reported case was an fsync to a FUSE network filesystem at ~100ms, which
// capped the whole collector at about 8 records/s (#35).
type slowSink struct {
	mu      sync.Mutex
	b       strings.Builder
	delay   time.Duration
	syncs   int
	written int
}

func (s *slowSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.written++
	return s.b.Write(p)
}

func (s *slowSink) Sync() error {
	time.Sleep(s.delay)
	s.mu.Lock()
	s.syncs++
	s.mu.Unlock()
	return nil
}

func (s *slowSink) lines() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Count(s.b.String(), "\n")
}

// LogJSON must not be serialized on the fsync.
//
// It used to hold the logger's single mutex across WriteString AND Sync, so
// all three workers queued behind one fsync and throughput was
// 1/fsync-latency however many workers ran. With a 50ms sync and 20 records
// that is a full second spent inside the callers; handing the line to a writer
// goroutine should cost them almost nothing.
//
// The assertion is deliberately loose - a tenth of the serialized cost - so it
// measures the difference between "serialized on disk" and "not", rather than
// the machine it runs on.
func TestLogJSONDoesNotBlockOnSync(t *testing.T) {
	const (
		records = 20
		delay   = 50 * time.Millisecond
	)
	sink := &slowSink{delay: delay}
	l := &Logger{}
	l.startCaptureWriter(sink, true) // per-record sync: the worst case

	start := time.Now()
	for i := 0; i < records; i++ {
		l.LogJSON("controller", okEvent)
	}
	elapsed := time.Since(start)

	serialized := records * delay
	if elapsed > serialized/10 {
		t.Fatalf("LogJSON took %v for %d records at a %v sync; serialized would be %v.\n"+
			"The callers are still waiting on the fsync.", elapsed, records, delay, serialized)
	}

	// And every record still lands: this buys throughput, not loss.
	l.Shutdown()
	if got := sink.lines(); got != records {
		t.Fatalf("capture holds %d records, want %d - records were dropped, not just deferred", got, records)
	}
}

// Concurrent callers must not serialize against each other either, which is
// the case that actually matters: three workers all reaching LogJSON.
func TestConcurrentLogJSONIsNotSerialized(t *testing.T) {
	const (
		callers = 3
		perCall = 10
		delay   = 40 * time.Millisecond
	)
	sink := &slowSink{delay: delay}
	l := &Logger{}
	l.startCaptureWriter(sink, true)

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perCall; j++ {
				l.LogJSON("controller", okEvent)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	serialized := callers * perCall * delay
	if elapsed > serialized/10 {
		t.Fatalf("%d callers took %v; serialized on a %v sync would be %v", callers, elapsed, delay, serialized)
	}

	l.Shutdown()
	if got := sink.lines(); got != callers*perCall {
		t.Fatalf("capture holds %d records, want %d", got, callers*perCall)
	}
}

// interval is the default, and "record" is what asks for the old per-record
// fsync. The knob decides durability, so it is worth asserting rather than
// assuming.
func TestJsonSyncPolicy(t *testing.T) {
	for _, tc := range []struct {
		cfg       string
		syncEvery bool
	}{
		{"", false},
		{"interval", false},
		{"record", true},
	} {
		sink := &slowSink{}
		l := &Logger{}
		l.startCaptureWriter(sink, tc.cfg == "record")
		if l.syncEvery != tc.syncEvery {
			t.Errorf("json_sync %q -> syncEvery=%v, want %v", tc.cfg, l.syncEvery, tc.syncEvery)
		}
		l.Shutdown()
	}
}
