package faro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

// A record written late must still say when Faro SAW the change.
//
// eventTime used to be stamped inside logJSONEvent, which runs on a worker
// after the work queue. Under any backlog every record was therefore dated
// when a worker reached it. Measured on a lab whose output volume could not
// keep up: the queue ran four minutes behind and every eventTime in that
// window was four minutes late. A timeline built from the capture is shifted
// by the backlog, and a burst is compressed into the order it drained - which
// is the one thing a capture exists to get right.
//
// This drives reconcile with an item observed four minutes ago, exactly as a
// backlogged queue would hand it over, and asserts the written record carries
// that time and not the time of the write.
func TestEventTimeIsObservationNotProcessing(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{OutputDir: dir, LogLevel: "info", JsonExport: true}
	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	c := &Controller{logger: logger, config: cfg}

	// Empty lister: the object is already gone, so this takes the snapshot
	// path. Either path writes the record; this one needs no live cache.
	idx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	c.listers.Store("v1/configmaps@test-ns", cache.NewGenericLister(idx, schema.GroupResource{Resource: "configmaps"}))

	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("v1")
	obj.SetKind("ConfigMap")
	obj.SetName("late-1")
	obj.SetNamespace("test-ns")
	obj.SetUID("uid-late-1")

	observed := time.Now().UTC().Add(-4 * time.Minute)
	if err := c.reconcile(&WorkItem{
		Key: "test-ns/late-1", informerNamespace: "test-ns",
		GVRString:  "v1/configmaps",
		EventType:  "ADDED",
		Configs:    []NormalizedConfig{{GVR: "v1/configmaps"}},
		Object:     obj,
		ObservedAt: observed,
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	logger.Shutdown()

	files, _ := filepath.Glob(filepath.Join(dir, "logs", "events-*.json"))
	var found bool
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var e JSONEvent
			if json.Unmarshal([]byte(line), &e) != nil || e.Name != "late-1" {
				continue
			}
			found = true
			got, err := time.Parse(time.RFC3339Nano, e.EventTime)
			if err != nil {
				t.Fatalf("eventTime %q is not RFC3339Nano: %v", e.EventTime, err)
			}
			if d := got.Sub(observed); d < -time.Second || d > time.Second {
				t.Fatalf("eventTime = %v, want the observation time %v (off by %v).\n"+
					"A record written after a backlog must be dated when Faro saw the change, "+
					"not when a worker got to it.", got, observed, d)
			}
		}
	}
	if !found {
		t.Fatal("no record written for late-1 - the function under test did not run")
	}
}
