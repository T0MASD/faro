package faro

import (
	"encoding/json"
	"fmt"
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

// Faro is meant to be pointed at a GVR before the type exists: start it, install the CRD
// later, and the objects are captured. Discovery cannot describe a type that is not
// installed, so requiring it means the informer is never started and the arrival is missed.
// These tests drive the two seams that decide it without needing a cluster.

func TestResourceInfoFromConfig(t *testing.T) {
	cfg := &Config{
		Resources: []ResourceConfig{
			{GVR: "example.com/v1/widgets", Scope: ClusterScope},
			{GVR: "example.com/v1/gadgets", Scope: NamespaceScope},
		},
	}
	c := &Controller{config: cfg}

	for _, tc := range []struct {
		name       string
		gvr        string
		wantGroup  string
		wantVer    string
		wantRes    string
		namespaced bool
		wantNil    bool
	}{
		{name: "group version resource", gvr: "example.com/v1/gadgets", wantGroup: "example.com", wantVer: "v1", wantRes: "gadgets", namespaced: true},
		{name: "cluster scope from config", gvr: "example.com/v1/widgets", wantGroup: "example.com", wantVer: "v1", wantRes: "widgets", namespaced: false},
		{name: "core group", gvr: "v1/configmaps", wantVer: "v1", wantRes: "configmaps", namespaced: true},
		{name: "not configured is namespaced", gvr: "other.io/v1beta1/things", wantGroup: "other.io", wantVer: "v1beta1", wantRes: "things", namespaced: true},
		{name: "unparseable", gvr: "nonsense", wantNil: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := c.resourceInfoFromConfig(tc.gvr)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil for %q, got %+v", tc.gvr, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected resource info for %q, got nil", tc.gvr)
			}
			if got.Group != tc.wantGroup || got.Version != tc.wantVer || got.Resource != tc.wantRes {
				t.Fatalf("parsed %q as %q/%q/%q", tc.gvr, got.Group, got.Version, got.Resource)
			}
			if got.Namespaced != tc.namespaced {
				t.Fatalf("%q namespaced=%t, want %t", tc.gvr, got.Namespaced, tc.namespaced)
			}
		})
	}
}

// An endpoint can serve resources and no usable discovery at all. KubeEdge's metaServer
// answers /apis with protobuf bytes under Content-Type: application/json, which the
// discovery client's JSON decoder rejects with "invalid character 'k'". The resource
// endpoints are ordinary JSON, so every configured GVR is still watchable - and before this
// change a discovery error stopped the controller before it started a single informer.
func TestStartsInformersWhenDiscoveryIsUnusable(t *testing.T) {
	const (
		namespace = "demo"
		listPath  = "/apis/example.com/v1/namespaces/demo/things"
	)
	var listed, watched atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, listPath) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("watch") == "true" {
				watched.Store(true)
				w.WriteHeader(http.StatusOK)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				<-r.Context().Done() // hold the watch open until the informer stops
				return
			}
			listed.Store(true)
			json.NewEncoder(w).Encode(map[string]any{
				"apiVersion": "example.com/v1",
				"kind":       "ThingList",
				"metadata":   map[string]any{"resourceVersion": "1"},
				"items":      []any{},
			})
			return
		}
		// Discovery, exactly as metaServer breaks it: protobuf labelled as JSON.
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "k8s\x00\x12\x04v1\x1a\x0cAPIGroupList")
	}))
	defer srv.Close()

	cfg := &Config{
		OutputDir: t.TempDir(),
		LogLevel:  "info",
		Resources: []ResourceConfig{{
			GVR:            "example.com/v1/things",
			Scope:          NamespaceScope,
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
	if _, err := disco.ServerGroups(); err == nil {
		t.Fatal("the fake endpoint must break discovery, otherwise this test proves nothing")
	}

	controller := NewController(&KubernetesClient{Dynamic: dyn, Discovery: disco, Config: restCfg}, logger, cfg)
	if err := controller.Start(); err != nil {
		t.Fatalf("unusable discovery stopped the controller: %v", err)
	}
	defer controller.Stop()

	if builtin, _ := controller.GetActiveInformers(); builtin != 1 {
		t.Fatalf("started %d config-driven informers, want 1: the configuration names the GVR in full", builtin)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if listed.Load() && watched.Load() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("informer did not reach the resource endpoint (listed=%t watched=%t)", listed.Load(), watched.Load())
}
