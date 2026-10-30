package faro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

// An ADDED must survive the object being deleted before the worker reaches it.
//
// reconcile() resolves every work item through the informer's lister, which
// answers for the CURRENT state. Churn that creates an object and deletes it
// ~0.1s later routinely outruns the queue, and the lister then says "not
// found" for something the API confirmed. The event used to be dropped at
// Debug, so the capture showed the object being deleted and never created -
// 220 of 230 creations in one measured scenario, with 0 of 230 deletions
// missing, because DELETED already carried its own metadata and ADDED carried
// nothing.
//
// The lister here is empty, which is exactly the state that produced the bug:
// the object is gone. Only the snapshot can answer.
func TestAddedSurvivesObjectDeletedBeforeReconcile(t *testing.T) {
	events := func(t *testing.T, withSnapshot bool) []JSONEvent {
		t.Helper()
		dir := t.TempDir()
		cfg := &Config{OutputDir: dir, LogLevel: "info", JsonExport: true}
		logger, err := NewLogger(cfg)
		if err != nil {
			t.Fatalf("logger: %v", err)
		}
		c := &Controller{logger: logger, config: cfg}

		// An EMPTY indexer: every Get returns not-found, which is the state
		// reconcile finds when the object has already been deleted.
		idx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
		c.listers.Store("v1/configmaps@test-ns", cache.NewGenericLister(idx, schema.GroupResource{Resource: "configmaps"}))

		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion("v1")
		obj.SetKind("ConfigMap")
		obj.SetName("churn-1")
		obj.SetNamespace("test-ns")
		obj.SetUID("uid-churn-1")

		item := &WorkItem{
			Key: "test-ns/churn-1", informerNamespace: "test-ns",
			GVRString: "v1/configmaps",
			EventType: "ADDED",
			Configs:   []NormalizedConfig{{GVR: "v1/configmaps"}},
		}
		if withSnapshot {
			item.Object = obj
		}
		if err := c.reconcile(item); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		logger.Shutdown()

		files, _ := filepath.Glob(filepath.Join(dir, "logs", "events-*.json"))
		var out []JSONEvent
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
				if err := json.Unmarshal([]byte(line), &e); err == nil {
					out = append(out, e)
				}
			}
		}
		return out
	}

	t.Run("with the snapshot, the creation is recorded", func(t *testing.T) {
		captured := events(t, true)
		var added *JSONEvent
		for i := range captured {
			if captured[i].EventType == "ADDED" && captured[i].Name == "churn-1" {
				added = &captured[i]
				break
			}
		}
		if added == nil {
			t.Fatal("ADDED for churn-1 is not in the capture - the creation was lost")
		}
		if added.Namespace != "test-ns" {
			t.Errorf("namespace = %q, want test-ns", added.Namespace)
		}
		if added.GVR != "v1/configmaps" {
			t.Errorf("gvr = %q, want v1/configmaps", added.GVR)
		}
	})

	// Without a snapshot there is nothing to record, and that is the behaviour
	// this fix replaces. Asserted so the test fails if the snapshot ever stops
	// being what makes the difference.
	t.Run("without the snapshot, it is still lost", func(t *testing.T) {
		for _, e := range events(t, false) {
			if e.EventType == "ADDED" && e.Name == "churn-1" {
				t.Fatal("ADDED recorded with no snapshot - the test no longer proves anything")
			}
		}
	})
}
