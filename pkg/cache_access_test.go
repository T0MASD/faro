package faro

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	clienttesting "k8s.io/client-go/testing"
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

// Before an informer's first list completes its cache is empty, and an empty
// answer then would say "nothing exists" when the truth is "not known yet". A
// consumer reconciling from it would close everything it holds. Two windows:
// the informer not created yet (it waits for the type to be served - here,
// the serving probe hangs) and created but its first list not answered.
func TestListBeforeSyncIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block int64 // the list call that hangs: 1 the serving probe, 2 the informer's own
	}{{"informer not created yet", 1}, {"first list in progress", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			var lists atomic.Int64
			dyn := newFakeAPI()
			dyn.PrependReactor("list", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
				if lists.Add(1) == tc.block {
					<-release // the API server has not answered yet
				}
				return false, nil, nil
			})
			defer close(release)
			_, c := startOnFakeAPI(t, dyn, &Config{})
			if !waitFor(t, 5*time.Second, func() bool { return lists.Load() >= tc.block }) {
				t.Fatalf("list call %d never came (%d lists)", tc.block, lists.Load())
			}
			if _, err := c.List("v1/configmaps", "t"); !errors.Is(err, ErrNotSynced) {
				t.Errorf("List: err = %v, want ErrNotSynced", err)
			}
			if _, err := c.Get("v1/configmaps", "t/a"); !errors.Is(err, ErrNotSynced) {
				t.Errorf("Get: err = %v, want ErrNotSynced", err)
			}
			if _, err := c.Get("v1/secrets", "t/a"); !errors.Is(err, ErrNotWatched) {
				t.Errorf("Get of an unwatched type: err = %v, want ErrNotWatched", err)
			}
		})
	}
}

// A watch that expires makes the informer list again, and every object comes
// back - the ones that did not change with the resourceVersion they had.
//
// Those were delivered, and captured, as UPDATED: a change that did not
// happen, once per object per lost watch. They are a resync: RESYNC to
// handlers, nothing in the capture.
func TestRelistOfUnchangedObjectsIsNotAnUpdate(t *testing.T) {
	var watches atomic.Int64
	dyn := newFakeAPI()
	dyn.PrependWatchReactor("configmaps", func(clienttesting.Action) (bool, watch.Interface, error) {
		if watches.Add(1) > 1 {
			return false, nil, nil // later watches: the tracker's own
		}
		w := watch.NewFake()
		go func() { // the first one expires, as an etcd compaction makes it
			time.Sleep(500 * time.Millisecond)
			w.Error(&metav1.Status{Status: metav1.StatusFailure, Code: http.StatusGone, Reason: metav1.StatusReasonExpired})
		}()
		return true, w, nil
	})
	if err := dyn.Tracker().Add(testConfigMap("steady", 0)); err != nil {
		t.Fatal(err)
	}
	h := &countingHandler{n: map[string]int{}}
	dir := t.TempDir()
	_, c := startOnFakeAPI(t, dyn, &Config{OutputDir: dir, JsonExport: true}, h)
	if !waitFor(t, 15*time.Second, func() bool { return watches.Load() >= 2 }) {
		t.Fatalf("the informer never watched again after its watch expired (%d watches)", watches.Load())
	}
	time.Sleep(500 * time.Millisecond)
	c.logger.FlushCapture()

	restarted, updated := 0, 0
	for _, e := range captured(t, dir) {
		switch {
		case e.EventType == "WATCH_RESTARTED":
			restarted++
		case e.Name == "steady" && e.EventType != "ADDED":
			updated++
		}
	}
	if restarted == 0 {
		t.Fatal("no WATCH_RESTARTED in the capture: the informer did not re-list, so the test proves nothing")
	}
	if updated > 0 || h.count("UPDATED") > 0 {
		t.Errorf("an object that did not change was recorded as changed after the re-list: %d in the capture, "+
			"%d UPDATED to handlers", updated, h.count("UPDATED"))
	}
	if h.count("RESYNC") == 0 {
		t.Error("the re-listed object did not reach handlers as RESYNC")
	}
}
