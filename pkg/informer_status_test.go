package faro

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// A watcher's silence is ambiguous: an empty namespace and one the client may not read deliver the
// same nothing, and so does an endpoint that has stopped answering. OnMatched cannot express any of
// it, because by definition it never fires. These tests drive the seam that can, against a stub API
// server, and cover the case that is easy to get wrong — a watch that fails AFTER it has synced,
// which a consumer must be able to tell from one that never synced at all.

const (
	statusGroup    = "example.com"
	statusVersion  = "v1"
	statusResource = "widgets"
	statusGVR      = statusGroup + "/" + statusVersion + "/" + statusResource
)

// stubAPI serves discovery and one namespaced resource, and lets a test change the answer for a
// namespace while an informer is running.
type stubAPI struct {
	*httptest.Server
	mu      sync.Mutex
	items   map[string]int  // namespace -> objects to list
	refused map[string]bool // namespace -> answer 403
	watches map[string]chan struct{}
}

func newStubAPI(t *testing.T, namespaces ...string) *stubAPI {
	t.Helper()
	s := &stubAPI{items: map[string]int{}, refused: map[string]bool{}, watches: map[string]chan struct{}{}}
	for _, ns := range namespaces {
		s.watches[ns] = make(chan struct{})
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		gv := statusGroup + "/" + statusVersion
		switch r.URL.Path {
		case "/api":
			enc.Encode(map[string]any{"kind": "APIVersions", "versions": []string{"v1"}})
			return
		case "/api/v1":
			enc.Encode(map[string]any{"kind": "APIResourceList", "groupVersion": "v1", "resources": []any{}})
			return
		case "/apis":
			enc.Encode(map[string]any{
				"kind": "APIGroupList", "apiVersion": "v1",
				"groups": []any{map[string]any{
					"name":             statusGroup,
					"versions":         []any{map[string]any{"groupVersion": gv, "version": statusVersion}},
					"preferredVersion": map[string]any{"groupVersion": gv, "version": statusVersion},
				}},
			})
			return
		case "/apis/" + gv:
			enc.Encode(map[string]any{
				"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": gv,
				"resources": []any{map[string]any{
					"name": statusResource, "singularName": "widget", "namespaced": true,
					"kind": "Widget", "verbs": []string{"get", "list", "watch"},
				}},
			})
			return
		}

		ns := namespaceOf(r.URL.Path)
		if ns == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.mu.Lock()
		refused, count, watch := s.refused[ns], s.items[ns], s.watches[ns]
		s.mu.Unlock()

		if refused {
			w.WriteHeader(http.StatusForbidden)
			enc.Encode(map[string]any{
				"kind": "Status", "apiVersion": "v1", "metadata": map[string]any{},
				"status": "Failure", "reason": "Forbidden", "code": 403,
				"message": forbiddenMessage(ns),
			})
			return
		}
		if r.URL.Query().Get("watch") == "true" {
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select { // hold the watch open until the informer stops or the test withdraws access
			case <-r.Context().Done():
			case <-watch:
			}
			return
		}
		items := make([]any, 0, count)
		for i := 0; i < count; i++ {
			items = append(items, map[string]any{
				"apiVersion": gv, "kind": "Widget",
				"metadata": map[string]any{
					"name": fmt.Sprintf("widget-%d", i), "namespace": ns,
					"uid": fmt.Sprintf("uid-%s-%d", ns, i), "resourceVersion": "1",
				},
			})
		}
		enc.Encode(map[string]any{
			"apiVersion": gv, "kind": "WidgetList",
			"metadata": map[string]any{"resourceVersion": "1"}, "items": items,
		})
	}))
	t.Cleanup(s.Close) // LIFO: this runs after the controller's own cleanup has stopped the informers
	return s
}

// forbiddenMessage is shaped like the API server's, because the body is the only place the reason
// for a refusal — and the identity it was evaluated for — survives to the caller.
func forbiddenMessage(ns string) string {
	return fmt.Sprintf("%s.%s is forbidden: User %q cannot list resource %q in API group %q in the namespace %q",
		statusResource, statusGroup, "probe", statusResource, statusGroup, ns)
}

// withdraw refuses this namespace from now on and drops the open watch, so the informer re-lists
// and meets the refusal — which is what revoking access does to a running watcher.
func (s *stubAPI) withdraw(ns string) {
	s.mu.Lock()
	s.refused[ns] = true
	open := s.watches[ns]
	s.watches[ns] = make(chan struct{})
	s.mu.Unlock()
	close(open)
}

func namespaceOf(path string) string {
	const marker = "/namespaces/"
	i := strings.Index(path, marker)
	if i < 0 {
		return ""
	}
	rest := path[i+len(marker):]
	if j := strings.Index(rest, "/"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// recorder is the consumer: it keeps what it was told, in order, which is all faro owes it.
type recorder struct {
	mu     sync.Mutex
	synced map[string]int
	failed map[string]error
	order  []string
}

func newRecorder() *recorder {
	return &recorder{synced: map[string]int{}, failed: map[string]error{}}
}

func (r *recorder) OnInformerSynced(gvr, namespace string, resourceCount int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.synced[namespace] = resourceCount
	r.order = append(r.order, "synced:"+namespace)
}

func (r *recorder) OnInformerFailed(gvr, namespace string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed[namespace] = err
	r.order = append(r.order, "failed:"+namespace)
}

func (r *recorder) syncedCount(ns string) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.synced[ns]
	return n, ok
}

func (r *recorder) failure(ns string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed[ns]
}

func (r *recorder) sequence() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

func startAgainst(t *testing.T, s *stubAPI, rec *recorder, namespaces ...string) *Controller {
	t.Helper()
	cfg := &Config{
		OutputDir: t.TempDir(), LogLevel: "info",
		Resources: []ResourceConfig{{GVR: statusGVR, Scope: NamespaceScope, NamespaceNames: namespaces}},
	}
	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(logger.Shutdown)

	restCfg := &rest.Config{Host: s.URL}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("dynamic client: %v", err)
	}
	disco, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		t.Fatalf("discovery client: %v", err)
	}
	if _, err := disco.ServerGroups(); err != nil {
		t.Fatalf("the stub must serve usable discovery, or this test is about discovery: %v", err)
	}
	c := NewController(&KubernetesClient{Dynamic: dyn, Discovery: disco, Config: restCfg}, logger, cfg)
	c.AddInformerStatusHandler(rec)
	if err := c.Start(); err != nil {
		t.Fatalf("controller did not start: %v", err)
	}
	t.Cleanup(c.Stop)
	return c
}

func await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestInformerStatusTellsAFailedWatchFromAnEmptyOne(t *testing.T) {
	api := newStubAPI(t, "populated", "empty", "restricted")
	api.items["populated"] = 2
	api.items["empty"] = 0
	api.refused["restricted"] = true

	rec := newRecorder()
	startAgainst(t, api, rec, "populated", "empty", "restricted")

	await(t, "the populated namespace to sync", func() bool { _, ok := rec.syncedCount("populated"); return ok })
	if n, _ := rec.syncedCount("populated"); n != 2 {
		t.Fatalf("reported %d objects, want 2", n)
	}

	// The distinction the seam exists for: an empty namespace is SYNCED holding nothing, which is
	// a different outcome from never having listed, and neither produces an event.
	await(t, "the empty namespace to sync", func() bool { _, ok := rec.syncedCount("empty"); return ok })
	if n, _ := rec.syncedCount("empty"); n != 0 {
		t.Fatalf("empty namespace reported %d objects, want 0", n)
	}
	if err := rec.failure("empty"); err != nil {
		t.Fatalf("an empty namespace was reported as a failure, so emptiness reads as refusal: %v", err)
	}

	await(t, "the refused namespace to fail", func() bool { return rec.failure("restricted") != nil })
	if _, ok := rec.syncedCount("restricted"); ok {
		t.Fatal("an informer that never listed was reported as synced")
	}
	// The server's own message must survive: a consumer told only that "an error happened" cannot
	// tell a refusal from an unreachable endpoint, and the two call for opposite responses.
	if got := rec.failure("restricted").Error(); !strings.Contains(got, forbiddenMessage("restricted")) {
		t.Fatalf("the server's message body was lost:\n got: %q\nwant it to contain: %q",
			got, forbiddenMessage("restricted"))
	}
}

func TestInformerStatusReportsAWatchThatFailsAfterSyncing(t *testing.T) {
	api := newStubAPI(t, "watched")
	api.items["watched"] = 1

	rec := newRecorder()
	startAgainst(t, api, rec, "watched")

	await(t, "the initial sync", func() bool { _, ok := rec.syncedCount("watched"); return ok })
	if err := rec.failure("watched"); err != nil {
		t.Fatalf("the informer was already failing before access was withdrawn: %v", err)
	}

	api.withdraw("watched")

	await(t, "the failure after withdrawal", func() bool { return rec.failure("watched") != nil })
	if got := rec.failure("watched").Error(); !strings.Contains(strings.ToLower(got), "forbidden") {
		t.Fatalf("the withdrawal was not reported as a refusal: %q", got)
	}
	// Order is the whole signal here. A consumer holding only "synced once" and "failed once" with
	// no sequence cannot tell access withdrawn from access that was never granted.
	seq := rec.sequence()
	if len(seq) < 2 || seq[0] != "synced:watched" || seq[len(seq)-1] != "failed:watched" {
		t.Fatalf("expected a sync followed by a failure, got %v", seq)
	}
}
