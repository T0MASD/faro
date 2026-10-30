package faro

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A consumer that reconciles needs the current state, not only the changes.
//
// The informers hold it - every watched object, kept current - but only behind
// the controller: the listers were private, so the one way to learn what
// exists was to have listened since the start and kept a copy. A consumer that
// restarts, or that lost track of what it did with an event, had nothing to
// reconcile against short of listing the API itself, which is the load the
// cache exists to save.
func TestListAndGetReadTheCache(t *testing.T) {
	dyn, c := startWithFakeAPI(t, &Config{})
	cms := dyn.Resource(testConfigMaps).Namespace("t")
	ctx := context.Background()
	for _, n := range []string{"a", "b", "c"} {
		if _, err := cms.Create(ctx, testConfigMap(n, 1), metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if !waitFor(t, 5*time.Second, func() bool { l, _ := c.List("v1/configmaps", "t"); return len(l) == 3 }) {
		l, err := c.List("v1/configmaps", "t")
		t.Fatalf("List = %d objects (err %v), want 3", len(l), err)
	}

	got, err := c.Get("v1/configmaps", "t/b")
	if err != nil || got == nil || got.GetName() != "b" || seqOf(got) != 1 {
		t.Fatalf("Get t/b = %v, %v", got, err)
	}
	// A copy: writing to it must not reach the cache.
	got.SetAnnotations(map[string]string{"seq": "99"})
	if again, _ := c.Get("v1/configmaps", "t/b"); seqOf(again) != 1 {
		t.Error("Get handed out the cached object itself; a change to it reached the cache")
	}

	if missing, err := c.Get("v1/configmaps", "t/nope"); missing != nil || err != nil {
		t.Errorf("Get of a missing object = %v, %v; want nil, nil", missing, err)
	}
	if _, err := c.List("v1/configmaps", "elsewhere"); !errors.Is(err, ErrNotWatched) {
		t.Errorf("List of an unwatched namespace: err = %v, want ErrNotWatched", err)
	}
}

// resync_sec hands every object to the handlers again, as RESYNC - and never
// to the capture.
//
// A RESYNC is the informer repeating what it already holds. Sent as UPDATED it
// would read as a change that did not happen, and in the capture it would add
// one record per object per period: on a capture-everything config, thousands
// of records an hour saying nothing.
func TestResyncReachesHandlersNotTheCapture(t *testing.T) {
	h := &countingHandler{n: map[string]int{}}
	dir := t.TempDir()
	cfg := &Config{OutputDir: dir, JsonExport: true, ResyncSec: 1}
	dyn, _ := startWithFakeAPI(t, cfg, h)
	if _, err := dyn.Resource(testConfigMaps).Namespace("t").Create(context.Background(), testConfigMap("steady", 0), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 10*time.Second, func() bool { return h.count("RESYNC") >= 2 }) {
		t.Fatalf("RESYNC delivered %d times in 10s with resync_sec 1", h.count("RESYNC"))
	}
	if n := h.count("UPDATED"); n != 0 {
		t.Errorf("an unchanged object was delivered as UPDATED %d times", n)
	}
	if n := h.count("ADDED"); n != 1 {
		t.Errorf("ADDED delivered %d times, want 1", n)
	}

	// The capture has the creation and nothing for the resyncs.
	time.Sleep(1500 * time.Millisecond) // the writer syncs once a second
	for _, e := range captured(t, dir) {
		if e.EventType == "RESYNC" || (e.Name == "steady" && e.EventType == "UPDATED") {
			t.Fatalf("the capture recorded a resync as %s", e.EventType)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "logs", "events-*.json"))
	if len(files) == 0 {
		t.Fatal("no capture file written - the test proves nothing")
	}
}
