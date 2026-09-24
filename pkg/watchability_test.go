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
