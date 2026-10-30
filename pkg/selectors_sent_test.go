package faro

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// fakeAPIServer answers discovery, lists and watches over real HTTP, and
// records the query of every list and watch of configmaps - what the API
// server is actually asked for, which is where a selector either takes effect
// or does not.
type fakeAPIServer struct {
	*httptest.Server
	mu      sync.Mutex
	queries []string // "list ?..." / "watch ?..."
}

func newFakeAPIServer(t *testing.T) *fakeAPIServer {
	t.Helper()
	f := &fakeAPIServer{}
	send := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/api":
			send(w, map[string]any{"kind": "APIVersions", "versions": []string{"v1"}})
		case p == "/apis":
			send(w, map[string]any{"kind": "APIGroupList", "apiVersion": "v1", "groups": []any{}})
		case p == "/api/v1":
			send(w, map[string]any{"kind": "APIResourceList", "groupVersion": "v1", "resources": []any{
				map[string]any{"name": "configmaps", "namespaced": true, "kind": "ConfigMap", "verbs": []string{"get", "list", "watch"}}}})
		case r.URL.Query().Get("watch") == "true":
			if strings.HasSuffix(p, "/configmaps") {
				f.record("watch", r)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done() // a watch with nothing to say, until the client goes
		default: // a list of anything, empty
			if strings.HasSuffix(p, "/configmaps") && r.URL.Query().Get("limit") != "1" { // not the serving probe
				f.record("list", r)
			}
			send(w, map[string]any{"kind": "List", "apiVersion": "v1", "metadata": map[string]any{"resourceVersion": "1"}, "items": []any{}})
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAPIServer) record(verb string, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, verb+" "+r.URL.Path+" "+r.URL.RawQuery)
}

func (f *fakeAPIServer) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...)
}

// controllerFromYAML builds a controller the way the binary does - a YAML
// config, strictly loaded - against the fake API server.
func controllerFromYAML(t *testing.T, srv *fakeAPIServer, body string) (*Controller, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("output_dir: "+dir+"\nlog_level: warning\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	if err := cfg.LoadFromYAML(path); err != nil {
		return nil, err
	}
	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatal(err)
	}
	restCfg := &rest.Config{Host: srv.URL}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		t.Fatal(err)
	}
	disc, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		t.Fatal(err)
	}
	c := NewController(&KubernetesClient{Dynamic: dyn, Discovery: disc, Config: restCfg}, logger, cfg)
	t.Cleanup(func() { c.Stop(); logger.Shutdown() })
	return c, c.Start()
}

// name_selector and field_selector must reach the API server as the list's
// and the watch's fieldSelector, from either config format.
//
// name_selector was stored and never sent, on a resource entry and inside a
// namespace entry alike, and field_selector did not exist: a config that
// asked for one object watched every object of the type, with nothing to say
// so.
func TestSelectorsReachTheAPIServer(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"resource entry", `resources:
  - gvr: v1/configmaps
    namespace_names: [t]
    name_selector: cm-a
    field_selector: metadata.namespace=t
    label_selector: app=x
`, "fieldSelector=metadata.name%3Dcm-a%2Cmetadata.namespace%3Dt&labelSelector=app%3Dx"},
		{"namespace entry", `namespaces:
  - name_selector: t
    resources:
      v1/configmaps:
        name_selector: cm-b
`, "fieldSelector=metadata.name%3Dcm-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeAPIServer(t)
			if _, err := controllerFromYAML(t, srv, tc.yaml); err != nil {
				t.Fatalf("start: %v", err)
			}
			var lists, watches int
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				lists, watches = 0, 0
				for _, q := range srv.seen() {
					if strings.HasPrefix(q, "list ") {
						lists++
					} else {
						watches++
					}
				}
				if lists > 0 && watches > 0 {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if lists == 0 || watches == 0 {
				t.Fatalf("the informer did not list and watch (%v)", srv.seen())
			}
			for _, q := range srv.seen() {
				if !strings.Contains(q, tc.want) {
					t.Errorf("%s - want %s in the query", q, tc.want)
				}
			}
		})
	}
}

// Two entries for one type and namespace asking for different label
// selectors cannot both be served by the one informer faro builds for them,
// and must fail the start rather than watch with the first entry's alone.
func TestConflictingSelectorsFailTheStart(t *testing.T) {
	srv := newFakeAPIServer(t)
	_, err := controllerFromYAML(t, srv, `resources:
  - gvr: v1/configmaps
    namespace_names: [t]
    label_selector: app=web
  - gvr: v1/configmaps
    namespace_names: [t]
    label_selector: app=db
`)
	if err == nil || !strings.Contains(err.Error(), "different selectors") {
		t.Fatalf("Start with two label selectors for one informer: err = %v", err)
	}
}
