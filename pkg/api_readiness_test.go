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

func apiService(group string, available bool) map[string]any {
	st := "False"
	if available {
		st = "True"
	}
	return map[string]any{
		"apiVersion": "apiregistration.k8s.io/v1", "kind": "APIService",
		"metadata": map[string]any{"name": "v1." + group, "resourceVersion": "2"},
		"spec":     map[string]any{"group": group, "version": "v1"},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Available", "status": st},
		}},
	}
}

// The wait must be driven by the API server's own transition, not by a timer.
//
// This matters beyond tidiness. While a GVR is waiting it is not watched, and an
// informer's initial LIST returns current state rather than history - so anything
// created and deleted inside the wait is never recorded at all, and anything
// created then modified arrives with the transition already collapsed. A 15s poll
// means up to 15s of a cluster's bring-up is invisible; 184 of 533 informers were
// waiting in the last measured run, covering exactly the addon install the capture
// exists to record.
//
// The backstop interval is set far longer than the test's patience, so passing
// proves the APIService event did the waking.
func TestReadinessSubscriptionWakesWaitersWithoutPolling(t *testing.T) {
	const namespace = "demo"
	var available atomic.Bool
	var listed, watched atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		unavailable := func() {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]any{
				"kind": "Status", "apiVersion": "v1", "status": "Failure",
				"message": "the server is currently unable to handle the request",
				"reason":  "ServiceUnavailable", "code": 503,
			})
		}
		switch {
		// The aggregated API: 503 on both discovery and resources until available.
		case r.URL.Path == "/apis/example.com/v1",
			strings.HasPrefix(r.URL.Path, "/apis/example.com/v1/"):
			if !available.Load() {
				unavailable()
				return
			}
			if r.URL.Path == "/apis/example.com/v1" {
				json.NewEncoder(w).Encode(map[string]any{
					"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "example.com/v1",
					"resources": []any{map[string]any{
						"name": "things", "namespaced": true, "kind": "Thing",
						"verbs": []any{"get", "list", "watch"},
					}},
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

		// The APIService collection, which is how readiness is announced.
		case r.URL.Path == "/apis/apiregistration.k8s.io/v1/apiservices":
			if r.URL.Query().Get("watch") == "true" {
				w.WriteHeader(http.StatusOK)
				f, _ := w.(http.Flusher)
				if f != nil {
					f.Flush()
				}
				for {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(50 * time.Millisecond):
					}
					if available.Load() {
						json.NewEncoder(w).Encode(map[string]any{
							"type": "MODIFIED", "object": apiService("example.com", true),
						})
						if f != nil {
							f.Flush()
						}
						<-r.Context().Done()
						return
					}
				}
			}
			json.NewEncoder(w).Encode(map[string]any{
				"apiVersion": "apiregistration.k8s.io/v1", "kind": "APIServiceList",
				"metadata": map[string]any{"resourceVersion": "1"},
				"items":    []any{apiService("example.com", false)},
			})
		case r.URL.Path == "/apis/apiregistration.k8s.io/v1":
			json.NewEncoder(w).Encode(map[string]any{
				"kind": "APIResourceList", "apiVersion": "v1",
				"groupVersion": "apiregistration.k8s.io/v1",
				"resources": []any{map[string]any{
					"name": "apiservices", "namespaced": false, "kind": "APIService",
					"verbs": []any{"get", "list", "watch"},
				}},
			})
		case r.URL.Path == "/apis":
			json.NewEncoder(w).Encode(map[string]any{
				"kind": "APIGroupList", "apiVersion": "v1",
				"groups": []any{
					map[string]any{"name": "example.com",
						"versions":         []any{map[string]any{"groupVersion": "example.com/v1", "version": "v1"}},
						"preferredVersion": map[string]any{"groupVersion": "example.com/v1", "version": "v1"}},
					map[string]any{"name": "apiregistration.k8s.io",
						"versions":         []any{map[string]any{"groupVersion": "apiregistration.k8s.io/v1", "version": "v1"}},
						"preferredVersion": map[string]any{"groupVersion": "apiregistration.k8s.io/v1", "version": "v1"}},
				},
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{
				"kind": "APIResourceList", "apiVersion": "v1", "resources": []any{},
			})
		}
	}))
	defer srv.Close()

	// Far longer than this test will wait. If the timer is what wakes the
	// waiter, this test fails.
	prev := watchabilityRecheck
	watchabilityRecheck = 10 * time.Minute
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

	time.Sleep(500 * time.Millisecond)
	if watched.Load() || listed.Load() {
		t.Fatal("reached an aggregated API whose backend was still 503ing")
	}
	if n := status.failed.Load(); n > 0 {
		t.Fatalf("built an informer before the API was available: %d failures", n)
	}

	// The backend comes up and the APIService reports Available=True.
	start := time.Now()
	available.Store(true)
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		if listed.Load() && watched.Load() {
			t.Logf("woken %s after the APIService became Available (backstop was %s)",
				time.Since(start).Round(time.Millisecond), watchabilityRecheck)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("not woken by the APIService event (listed=%t watched=%t); only the %s backstop could have",
		listed.Load(), watched.Load(), watchabilityRecheck)
}

// The registry must only answer when it has positive evidence. Absence of an
// entry is not evidence of absence: concluding "not served" from a missing
// APIService would make this a single point of failure for every informer in
// the process - a failed watch or a partial list and nothing ever starts.
func TestRegistryAnswersOnlyFromPositiveEvidence(t *testing.T) {
	r := newAPIReadiness()

	if _, known := r.state("example.com", "v1", "things"); known {
		t.Fatal("answered before the informers had listed")
	}
	r.markSynced()

	if _, known := r.state("example.com", "v1", "things"); known {
		t.Fatal("answered for a groupVersion it has never seen; must fall back to the API")
	}

	// Positive evidence: the APIService exists and says it is not Available.
	r.gvAvailable["example.com/v1"] = false
	ready, known := r.state("example.com", "v1", "things")
	if !known || ready {
		t.Fatalf("APIService Available=False should be a definite not-ready, got ready=%t known=%t", ready, known)
	}

	r.gvAvailable["example.com/v1"] = true
	if ready, known := r.state("example.com", "v1", "things"); !known || !ready {
		t.Fatalf("APIService Available=True should be ready, got ready=%t known=%t", ready, known)
	}

	// A CRD in an available group that has not established yet is still not
	// ready: APIService is per group/version, Established is per resource, and
	// velero's two groupVersions carry 13 CRDs between them.
	r.crdSeen["example.com/things"] = true
	r.crdEstab["example.com/things"] = false
	if ready, known := r.state("example.com", "v1", "things"); !known || ready {
		t.Fatalf("unestablished CRD in an available group should not be ready, got ready=%t known=%t", ready, known)
	}
	r.crdEstab["example.com/things"] = true
	if ready, known := r.state("example.com", "v1", "things"); !known || !ready {
		t.Fatalf("established CRD in an available group should be ready, got ready=%t known=%t", ready, known)
	}

	// A sibling resource in the same group is unaffected by that CRD.
	if ready, known := r.state("example.com", "v1", "others"); !known || !ready {
		t.Fatalf("sibling resource should follow the group, got ready=%t known=%t", ready, known)
	}
}
