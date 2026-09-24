package faro

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// An informer does an initial LIST and then WATCHes from that resourceVersion, so it
// needs both verbs. The predicate this replaces asked for `watch || list` and was never
// called from anywhere, which let a get,list resource through twice over.
func TestIsResourceWatchableRequiresListAndWatch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		verbs []string
		want  bool
	}{
		{"list and watch", []string{"get", "list", "watch"}, true},
		{"list only", []string{"get", "list"}, false},
		{"watch only", []string{"get", "watch"}, false},
		{"neither", []string{"get", "create"}, false},
		{"empty but reported", []string{}, false},
		// nil is "discovery did not say", which is the CRD-not-installed-yet case.
		// Reading it as unwatchable would stop Faro ever picking up a type that
		// arrives with its addon, which is most types on a cluster being built.
		{"not discovered", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isResourceWatchable(tc.verbs); got != tc.want {
				t.Fatalf("isResourceWatchable(%v) = %t, want %t", tc.verbs, got, tc.want)
			}
		})
	}
}

// A resource the API serves but cannot watch must get no informer at all.
//
// stats.antrea.io and v1/componentstatuses advertise get,list and no watch. A reflector
// pointed at one does not give up - it fails and retries for the life of the process.
// Measured on a capture-everything config: 228 failures every two minutes from seven
// such types, in the one log the capture exists to produce.
func TestNoInformerForUnwatchableResource(t *testing.T) {
	const namespace = "demo"
	var listed, watched atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/apis/example.com/v1/"):
			if r.URL.Query().Get("watch") == "true" {
				watched.Store(true)
			} else {
				listed.Store(true)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"apiVersion": "example.com/v1", "kind": "ThingList",
				"metadata": map[string]any{"resourceVersion": "1"}, "items": []any{},
			})
		case r.URL.Path == "/apis/example.com/v1":
			// Served, but get,list only - no watch.
			json.NewEncoder(w).Encode(map[string]any{
				"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "example.com/v1",
				"resources": []any{map[string]any{
					"name": "things", "namespaced": true, "kind": "Thing",
					"verbs": []any{"get", "list"},
				}},
			})
		case r.URL.Path == "/apis":
			json.NewEncoder(w).Encode(map[string]any{
				"kind": "APIGroupList", "apiVersion": "v1",
				"groups": []any{map[string]any{
					"name": "example.com",
					"versions": []any{map[string]any{
						"groupVersion": "example.com/v1", "version": "v1",
					}},
					"preferredVersion": map[string]any{
						"groupVersion": "example.com/v1", "version": "v1",
					},
				}},
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{
				"kind": "APIResourceList", "apiVersion": "v1", "resources": []any{},
			})
		}
	}))
	defer srv.Close()

	cfg := &Config{
		OutputDir: t.TempDir(),
		LogLevel:  "info",
		Resources: []ResourceConfig{{
			GVR: "example.com/v1/things", Scope: NamespaceScope,
			NamespaceNames: []string{namespace},
		}},
	}
	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	defer logger.Shutdown()

	restCfg := &rest.Config{Host: srv.URL}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("dynamic client: %v", err)
	}
	disco, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		t.Fatalf("discovery client: %v", err)
	}

	controller := NewController(&KubernetesClient{Dynamic: dyn, Discovery: disco, Config: restCfg}, logger, cfg)
	if err := controller.Start(); err != nil {
		t.Fatalf("controller did not start: %v", err)
	}
	defer controller.Stop()

	// Long enough that a reflector would have reached the endpoint several times.
	time.Sleep(2 * time.Second)

	if watched.Load() {
		t.Fatal("watched a resource the API says it cannot watch")
	}
	if listed.Load() {
		t.Fatal("listed a resource no informer should have been built for")
	}
}

// countingStatus records what the controller says about its informers.
type countingStatus struct {
	synced, failed atomic.Int64
	lastErr        atomic.Value
}

func (c *countingStatus) OnInformerSynced(gvr, namespace string, resourceCount int) {
	c.synced.Add(1)
}
func (c *countingStatus) OnInformerFailed(gvr, namespace string, err error) {
	c.failed.Add(1)
	if err != nil {
		c.lastErr.Store(err.Error())
	}
}

// An aggregated API is registered, and fully described in discovery, before the
// Deployment behind it is serving. The aggregation layer answers every request in
// that window with 503. Faro must wait it out rather than build an informer whose
// reflector burns retries on it.
//
// Measured on antrea: the APIService for controlplane.antrea.io is registered two
// seconds before antrea-controller's pod is created and does not go Available for
// another 35 seconds. Faro spent 104 reflector retries and 26 audited 503s there.
func TestWaitsForAggregatedApiToStartServing(t *testing.T) {
	const namespace = "demo"
	var unavailable atomic.Bool
	var listed, watched atomic.Bool
	var reqs503 atomic.Int64
	unavailable.Store(true)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/apis/example.com/v1/"):
			// The backend is not up yet: the aggregation layer's own answer.
			if unavailable.Load() {
				reqs503.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
				json.NewEncoder(w).Encode(map[string]any{
					"kind": "Status", "apiVersion": "v1", "status": "Failure",
					"message": "the server is currently unable to handle the request",
					"reason":  "ServiceUnavailable", "code": 503,
				})
				return
			}
			if r.URL.Query().Get("watch") == "true" {
				watched.Store(true)
				w.WriteHeader(http.StatusOK)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				<-r.Context().Done()
				return
			}
			listed.Store(true)
			json.NewEncoder(w).Encode(map[string]any{
				"apiVersion": "example.com/v1", "kind": "ThingList",
				"metadata": map[string]any{"resourceVersion": "1"}, "items": []any{},
			})
		case r.URL.Path == "/apis/example.com/v1":
			// Declared the whole time, watch included - that is the trap.
			json.NewEncoder(w).Encode(map[string]any{
				"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "example.com/v1",
				"resources": []any{map[string]any{
					"name": "things", "namespaced": true, "kind": "Thing",
					"verbs": []any{"get", "list", "watch"},
				}},
			})
		case r.URL.Path == "/apis":
			json.NewEncoder(w).Encode(map[string]any{
				"kind": "APIGroupList", "apiVersion": "v1",
				"groups": []any{map[string]any{
					"name": "example.com",
					"versions": []any{map[string]any{
						"groupVersion": "example.com/v1", "version": "v1",
					}},
					"preferredVersion": map[string]any{
						"groupVersion": "example.com/v1", "version": "v1",
					},
				}},
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{
				"kind": "APIResourceList", "apiVersion": "v1", "resources": []any{},
			})
		}
	}))
	defer srv.Close()

	prev := watchabilityRecheck
	watchabilityRecheck = 200 * time.Millisecond
	defer func() { watchabilityRecheck = prev }()

	cfg := &Config{
		OutputDir: t.TempDir(), LogLevel: "info",
		Resources: []ResourceConfig{{
			GVR: "example.com/v1/things", Scope: NamespaceScope,
			NamespaceNames: []string{namespace},
		}},
	}
	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	defer logger.Shutdown()

	restCfg := &rest.Config{Host: srv.URL}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("dynamic client: %v", err)
	}
	disco, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		t.Fatalf("discovery client: %v", err)
	}

	controller := NewController(&KubernetesClient{Dynamic: dyn, Discovery: disco, Config: restCfg}, logger, cfg)
	status := &countingStatus{}
	controller.AddInformerStatusHandler(status)
	if err := controller.Start(); err != nil {
		t.Fatalf("controller did not start: %v", err)
	}
	defer controller.Stop()

	// While the backend 503s there must be no informer, and so nothing to fail.
	// This is the whole point: an informer built during the outage does not wait,
	// it retries - 104 reflector failures against antrea's controlplane API in the
	// 35 seconds before its Deployment was Ready.
	time.Sleep(2 * time.Second)
	t.Logf("during outage: 503s served=%d informer failures reported=%d",
		reqs503.Load(), status.failed.Load())
	if n := status.failed.Load(); n > 0 {
		le, _ := status.lastErr.Load().(string)
		t.Fatalf("built an informer while the backend was returning 503: %d failures reported (last: %s)", n, le)
	}
	if watched.Load() {
		t.Fatal("watched an aggregated API whose backend was still returning 503")
	}

	// The backend comes up. Faro must notice without being restarted.
	unavailable.Store(false)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if listed.Load() && watched.Load() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("did not start watching after the backend became available (listed=%t watched=%t)",
		listed.Load(), watched.Load())
}
