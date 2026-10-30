package faro

import (
	"context"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

type deleteRecorder struct {
	mu  sync.Mutex
	got []*unstructured.Unstructured
}

func (d *deleteRecorder) OnMatched(e MatchedEvent) error {
	if e.EventType == "DELETED" {
		d.mu.Lock()
		d.got = append(d.got, e.Object)
		d.mu.Unlock()
	}
	return nil
}

func (d *deleteRecorder) deleted() []*unstructured.Unstructured {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*unstructured.Unstructured(nil), d.got...)
}

// A DELETED must say what was deleted.
//
// It reached handlers as a stub: name and namespace, measured from a library
// consumer as uid="" labels=map[] annotations=map[]. The capture recorded no
// labels either. The informer hands over the object as it was last seen - in
// the delete or its tombstone - and that was thrown away at the one moment a
// consumer most needs it: to know which of its things went, and in what state.
func TestDeletedCarriesTheLastState(t *testing.T) {
	h := &deleteRecorder{}
	dir := t.TempDir()
	dyn, _ := startWithFakeAPI(t, &Config{OutputDir: dir, JsonExport: true}, h)
	cms := dyn.Resource(testConfigMaps).Namespace("t")
	ctx := context.Background()
	if _, err := cms.Create(ctx, testConfigMap("gone", 3), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := cms.Delete(ctx, "gone", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return len(h.deleted()) > 0 }) {
		t.Fatal("no DELETED delivered")
	}
	d := h.deleted()[0]
	if d.GetName() != "gone" || d.GetNamespace() != "t" || string(d.GetUID()) != "uid-gone" {
		t.Errorf("DELETED identity = %s/%s uid %q", d.GetNamespace(), d.GetName(), d.GetUID())
	}
	if d.GetLabels()["app"] != "inbox" || seqOf(d) != 3 {
		t.Errorf("DELETED reached the handler without its last state: labels %v, annotations %v", d.GetLabels(), d.GetAnnotations())
	}

	time.Sleep(1500 * time.Millisecond) // the capture syncs once a second
	var rec *JSONEvent
	for _, e := range captured(t, dir) {
		if e.EventType == "DELETED" && e.Name == "gone" {
			e := e
			rec = &e
		}
	}
	if rec == nil {
		t.Fatal("DELETED not in the capture")
	}
	if rec.Labels["app"] != "inbox" || rec.UID != "uid-gone" {
		t.Errorf("captured DELETED: labels %v uid %q, want the object's", rec.Labels, rec.UID)
	}
}

// A DELETED reached after the name was reused must still describe the object
// that was deleted, not the one now in the cache under its name.
func TestDeletedIsNotConfusedWithARecreation(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{OutputDir: dir, LogLevel: "info", JsonExport: true}
	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := &deleteRecorder{}
	c := &Controller{logger: logger, config: cfg, eventHandlers: []EventHandler{h}}

	// The cache already holds the NEW object under the same name.
	idx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	newer := testConfigMap("reused", 9)
	newer.SetUID("uid-newer")
	newer.SetLabels(map[string]string{"app": "newer"})
	if err := idx.Add(newer); err != nil {
		t.Fatal(err)
	}
	c.listers.Store("v1/configmaps@t", cache.NewGenericLister(idx, schema.GroupResource{Resource: "configmaps"}))

	old := testConfigMap("reused", 2)
	old.SetUID("uid-older")
	if err := c.reconcile(&WorkItem{
		Key: "t/reused", informerNamespace: "t", GVRString: "v1/configmaps", EventType: "DELETED",
		Configs:    []NormalizedConfig{{GVR: "v1/configmaps"}},
		DeletedUID: "uid-older", DeletedResourceVersion: "3", Object: old,
	}); err != nil {
		t.Fatal(err)
	}
	logger.Shutdown()

	got := h.deleted()
	if len(got) != 1 || string(got[0].GetUID()) != "uid-older" || got[0].GetLabels()["app"] != "inbox" {
		t.Fatalf("handler got %v, want the deleted object (uid-older, app=inbox)", got)
	}
	for _, e := range captured(t, dir) {
		if e.Name == "reused" && (e.UID != "uid-older" || e.EventType != "DELETED") {
			t.Errorf("captured %s uid %q labels %v - the deletion was recorded as the new object", e.EventType, e.UID, e.Labels)
		}
	}
}

// A delete whose object faro never cached a UID for was dropped: the handler
// logged "No cached UID" and returned, so neither handlers nor the capture
// heard of it. The object carries its own UID - in a plain delete and in the
// tombstone client-go hands over when the delete itself was missed - and that
// is the one to use.
func TestDeleteWithoutACachedUIDIsDelivered(t *testing.T) {
	cfg := &Config{OutputDir: t.TempDir(), LogLevel: "warning"}
	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Shutdown()
	c := &Controller{logger: logger, config: cfg, metrics: NewMetricsCollector(MetricsConfig{}, *logger)}

	for _, tc := range []struct {
		name string
		wrap func(*unstructured.Unstructured) interface{}
	}{
		{"delete", func(o *unstructured.Unstructured) interface{} { return o }},
		{"tombstone", func(o *unstructured.Unstructured) interface{} {
			return cache.DeletedFinalStateUnknown{Key: "t/" + o.GetName(), Obj: o}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []*unstructured.Unstructured
			tracker := &InformerStateTracker{GVR: "v1/configmaps@t"} // no UID cached for it
			h := c.createStateTrackingEventHandlers(tracker, InformerConfig{GVRString: "v1/configmaps",
				HandlerFunc: func(ev string, o *unstructured.Unstructured) {
					if ev == "DELETED" {
						got = append(got, o)
					}
				}})
			o := testConfigMap("uncached-"+tc.name, 4)
			h.OnDelete(tc.wrap(o))
			if len(got) != 1 {
				t.Fatalf("a %s with no cached UID was delivered %d times, want 1", tc.name, len(got))
			}
			if string(got[0].GetUID()) != "uid-uncached-"+tc.name || got[0].GetLabels()["app"] != "inbox" {
				t.Errorf("delivered uid %q labels %v, want the object's own", got[0].GetUID(), got[0].GetLabels())
			}
		})
	}
}
