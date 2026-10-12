package faro

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A broken watch must appear in the CAPTURE, not only in the log.
//
// Before this, a watch that failed after sync was reported at Warning level and
// nowhere else: events-*.json carried only object events, so a consumer reading
// the capture could not tell a quiet resource from one whose watch had broken.
// Both look like an absence of events. Measured on a real run: 402 watches
// dropped, 300 of them in one second, and the capture said nothing.
func TestEmitWatchFailedReachesTheCapture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	l := &Logger{jsonFile: f}
	l.startCaptureWriter(f, true)
	c := &Controller{logger: l}
	c.emitWatchFailed("v1/pods", "kube-system", errors.New("connection reset by peer"))
	// The capture has its own writer goroutine; drain it before reading back.
	l.Shutdown()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(b))
	if line == "" {
		t.Fatal("nothing was written to the capture")
	}

	var ev JSONEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("capture line is not a JSONEvent: %v\nline: %s", err, line)
	}
	if ev.EventType != "WATCH_FAILED" {
		t.Errorf("EventType = %q, want WATCH_FAILED", ev.EventType)
	}
	if ev.GVR != "v1/pods" {
		t.Errorf("GVR = %q, want v1/pods", ev.GVR)
	}
	if ev.Namespace != "kube-system" {
		t.Errorf("Namespace = %q, want kube-system", ev.Namespace)
	}
	if got, _ := ev.Fields["error"].(string); !strings.Contains(got, "connection reset") {
		t.Errorf("Fields[error] = %q, want the server's error text", got)
	}
	if ev.EventTime == "" {
		t.Error("EventTime is empty; a failure with no time cannot be placed in a timeline")
	}
}
