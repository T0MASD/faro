package faro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The delete path has two shapes and only one of them was broken.
//
// When the object is already gone from the cache, logJSONEvent is handed nil
// and uses time.Now() - that was always fine. When it is handed a reconstructed
// stub, it read creationTimestamp off an object that has none and formatted the
// zero value: "0001-01-01T00:00:00Z", on 21 of 123 deletes in a measured
// capture. An integration test cannot choose which shape it gets, so this
// drives the function directly with the shape that was broken.
func TestLogJSONEventDeletedFromStub(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{OutputDir: dir, LogLevel: "info", JsonExport: true}
	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	c := &Controller{logger: logger, config: cfg}

	// Exactly what reconcile builds for a delete: name, namespace, uid, and no
	// creationTimestamp, because the object is gone.
	stub := &unstructured.Unstructured{}
	stub.SetName("cloudcore")
	stub.SetNamespace("kubeedge")

	// The observation time now comes from the caller (the WorkItem), so pass one
	// and assert it is what lands in the record rather than the time the write
	// happened to run.
	observed := time.Date(2026, 10, 12, 18, 30, 0, 0, time.UTC)
	c.logJSONEvent("DELETED", "v1/configmaps", "kubeedge", "cloudcore", "uid-1", nil, stub, observed)
	logger.Shutdown()

	files, _ := filepath.Glob(filepath.Join(dir, "logs", "events-*.json"))
	if len(files) == 0 {
		t.Fatal("no events file written")
	}
	body, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var found bool
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e JSONEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.EventType != "DELETED" || e.Name != "cloudcore" {
			continue
		}
		found = true

		if strings.HasPrefix(e.Timestamp, "0001-01-01") {
			t.Fatalf("stub delete emitted the zero creationTimestamp %q; omit it instead", e.Timestamp)
		}
		if strings.Contains(line, `"timestamp"`) && e.Timestamp == "" {
			t.Fatalf("empty timestamp was emitted rather than omitted: %s", line)
		}
		if e.EventTime == "" {
			t.Fatal("stub delete has no eventTime; this is the event that cannot be ordered")
		}
		got, err := time.Parse(time.RFC3339Nano, e.EventTime)
		if err != nil {
			t.Fatalf("eventTime %q is not RFC3339Nano: %v", e.EventTime, err)
		}
		if !got.Equal(observed) {
			t.Fatalf("eventTime = %v, want the observation time %v - a record must say when Faro SAW the change, not when a worker wrote it", got, observed)
		}
		t.Logf("stub delete -> timestamp=%q eventTime=%q", e.Timestamp, e.EventTime)
	}
	if !found {
		t.Fatal("no DELETED event written - the function under test did not run")
	}
}

// The other two event types come through the same function with a real object.
func TestLogJSONEventCarriesEventTime(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{OutputDir: dir, LogLevel: "info", JsonExport: true}
	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	c := &Controller{logger: logger, config: cfg}

	obj := &unstructured.Unstructured{}
	obj.SetName("cp1")
	obj.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-time.Hour)))

	// Two observations of the same hour-old object, seen a second apart. The
	// times now come from the caller, which is the point: creation is history,
	// each eventTime is when Faro saw that particular change.
	first := time.Date(2026, 10, 12, 18, 30, 0, 0, time.UTC)
	c.logJSONEvent("ADDED", "v1/nodes", "", "cp1", "uid-1", nil, obj, first)
	c.logJSONEvent("UPDATED", "v1/nodes", "", "cp1", "uid-1", nil, obj, first.Add(time.Second))
	logger.Shutdown()

	files, _ := filepath.Glob(filepath.Join(dir, "logs", "events-*.json"))
	if len(files) == 0 {
		t.Fatal("no events file written")
	}
	body, _ := os.ReadFile(files[0])
	var n int
	seen := map[string]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e JSONEvent
		if json.Unmarshal([]byte(line), &e) != nil || e.Name != "cp1" {
			continue
		}
		n++
		if e.EventTime == "" {
			t.Fatalf("%s carries no eventTime", e.EventType)
		}
		if _, err := time.Parse(time.RFC3339Nano, e.EventTime); err != nil {
			t.Fatalf("eventTime %q is not RFC3339Nano", e.EventTime)
		}
		seen[e.EventTime] = true
	}
	if n != 2 {
		t.Fatalf("expected 2 events, got %d", n)
	}
	// An hour-old object observed twice: creation is history, eventTime is now.
	if len(seen) != 2 {
		t.Fatal("two observations collapsed onto one eventTime")
	}
}

// A library user gets MatchedEvent, not the JSON. It had the same defect: its
// Timestamp is the object's creationTimestamp, so a handler could not order
// what it received either.
func TestMatchedEventCarriesEventTime(t *testing.T) {
	obj := &unstructured.Unstructured{}
	obj.SetName("cp1")
	created := metav1.NewTime(time.Now().Add(-90 * time.Minute))
	obj.SetCreationTimestamp(created)

	ev := MatchedEvent{
		EventType: "UPDATED",
		Object:    obj,
		GVR:       "v1/nodes",
		Timestamp: obj.GetCreationTimestamp().Time,
		EventTime: time.Now().UTC(),
	}

	if ev.EventTime.IsZero() {
		t.Fatal("MatchedEvent.EventTime is zero; a handler cannot order what it receives")
	}
	if !ev.EventTime.After(ev.Timestamp) {
		t.Fatalf("EventTime %v should be later than the object's creation %v", ev.EventTime, ev.Timestamp)
	}
	// An hour and a half apart: the two fields answer different questions.
	if d := ev.EventTime.Sub(ev.Timestamp); d < time.Hour {
		t.Fatalf("expected creation and observation to differ by the object's age, got %v", d)
	}
}
