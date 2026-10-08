package faro

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// A sink that records what it was given and can be closed, so a test can see
// each rotated file separately.
type recordingSink struct {
	mu     sync.Mutex
	b      strings.Builder
	closed bool
	name   string
}

func (s *recordingSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *recordingSink) Sync() error { return nil }
func (s *recordingSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
func (s *recordingSink) lines() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Count(s.b.String(), "\n")
}

// rotatingLogger wires a Logger to a sequence of in-memory sinks.
func rotatingLogger(t *testing.T, afterRecords int) (*Logger, func() []*recordingSink) {
	t.Helper()
	var mu sync.Mutex
	first := &recordingSink{name: "f0"}
	sinks := []*recordingSink{first}
	l := &Logger{}
	l.rotateRecords = afterRecords
	l.openNext = func() (captureSink, string, error) {
		mu.Lock()
		defer mu.Unlock()
		s := &recordingSink{name: "f" + string(rune('0'+len(sinks)))}
		sinks = append(sinks, s)
		return s, s.name, nil
	}
	l.startCaptureWriter(first, false)
	return l, func() []*recordingSink {
		mu.Lock()
		defer mu.Unlock()
		out := make([]*recordingSink, len(sinks))
		copy(out, sinks)
		return out
	}
}

// Every record must land exactly once, somewhere, across rotations.
//
// That is the only property that matters: a capture split across files is
// still one capture, and a reader concatenates them. A record lost at a
// boundary, or written to both the old and the new file, would be worse than
// not rotating at all.
func TestRotationLosesAndDuplicatesNothing(t *testing.T) {
	const (
		every   = 25
		records = 230
	)
	l, get := rotatingLogger(t, every)
	for i := 0; i < records; i++ {
		l.LogJSON("controller", okEvent)
	}
	l.Shutdown()

	sinks := get()
	total := 0
	for _, s := range sinks {
		total += s.lines()
	}
	if total != records {
		t.Fatalf("capture holds %d records across %d files, want %d", total, len(sinks), records)
	}
	if len(sinks) < records/every {
		t.Fatalf("only %d file(s) for %d records rotating every %d - rotation did not happen", len(sinks), records, every)
	}
	// Every file but the last holds exactly the threshold, and every rotated
	// file is closed. A file left open is a descriptor leak per rotation.
	for i, s := range sinks[:len(sinks)-1] {
		if n := s.lines(); n != every {
			t.Errorf("file %d holds %d records, want %d", i, n, every)
		}
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if !closed {
			t.Errorf("file %d was not closed when it was rotated away", i)
		}
	}
}

// FlushCapture must keep its meaning across a rotation: everything enqueued
// before it is on disk, wherever "disk" has moved to.
func TestFlushCaptureAcrossRotation(t *testing.T) {
	l, get := rotatingLogger(t, 10)
	for i := 0; i < 35; i++ {
		l.LogJSON("controller", okEvent)
	}
	l.FlushCapture()

	total := 0
	for _, s := range get() {
		total += s.lines()
	}
	if total != 35 {
		t.Fatalf("after FlushCapture the capture holds %d of 35 records across its files", total)
	}
	l.Shutdown()
}

// Shutdown during a rotation must still drain, and must close the file it ends
// on - not only the ones it rotated away from.
func TestShutdownClosesTheCurrentFile(t *testing.T) {
	l, get := rotatingLogger(t, 5)
	for i := 0; i < 12; i++ {
		l.LogJSON("controller", okEvent)
	}
	l.Shutdown()

	sinks := get()
	last := sinks[len(sinks)-1]
	last.mu.Lock()
	closed := last.closed
	last.mu.Unlock()
	if !closed {
		t.Fatal("the file the capture ended on was left open")
	}
}

// A Logger with no opener must not pretend to rotate.
func TestNoOpenerMeansNoRotation(t *testing.T) {
	sink := &recordingSink{}
	l := &Logger{rotateRecords: 5}
	l.startCaptureWriter(sink, false)
	for i := 0; i < 20; i++ {
		l.LogJSON("controller", okEvent)
	}
	l.Shutdown()
	if n := sink.lines(); n != 20 {
		t.Fatalf("capture holds %d records, want all 20 in the one sink", n)
	}
}

// An idle collector must not rotate: with nothing written there are no chunks
// to escape, and rotating would leave a trail of empty files.
func TestIdleDoesNotRotate(t *testing.T) {
	l, get := rotatingLogger(t, 0)
	l.rotateEvery = 10 * time.Millisecond
	time.Sleep(2*captureSyncInterval + 200*time.Millisecond)
	l.Shutdown()
	if n := len(get()); n != 1 {
		t.Fatalf("an idle writer produced %d capture files; it should still be on its first", n)
	}
}
