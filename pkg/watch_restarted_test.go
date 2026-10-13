package faro

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// A watch that dies MID-STREAM must reach the capture.
//
// This is the case SetWatchErrorHandler does not cover: client-go calls that
// handler only when ESTABLISHING the list/watch fails. A watch that was working
// and then stops is handled inside the reflector, which logs "watch ended with
// error" and quietly re-lists. Measured in a lab run across two control-plane
// kills: 400 broken watches logged, 0 recorded.
//
// Driven through a real client-go reflector against a real HTTP server, because
// the bug lives in the seam between faro and client-go - a fake that invokes
// faro's own callbacks would pass whether or not the seam is wired up.
func TestWatchLostMidStreamReachesTheCapture(t *testing.T) {
	const namespace = "demo"
	var lists, watches atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "things") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"kind": "APIResourceList", "apiVersion": "v1", "resources": []any{},
			})
			return
		}
		if r.URL.Query().Get("watch") == "true" {
			// Accept the watch, send nothing, then hang up. That is what a
			// control plane going away looks like to a reflector that has
			// already synced.
			watches.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		lists.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"kind": "ThingList", "apiVersion": "example.com/v1",
			"metadata": map[string]any{"resourceVersion": "1"},
			"items":    []any{},
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := &Config{OutputDir: dir, LogLevel: "info", JsonExport: true}
	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatal(err)
	}
	restCfg := &rest.Config{Host: srv.URL}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &Controller{
		logger: logger, config: cfg, ctx: ctx,
		client:  &KubernetesClient{Dynamic: dyn, Config: restCfg},
		metrics: NewMetricsCollector(MetricsConfig{}, *logger),
	}

	inf, err := c.createNamespaceSpecificInformer(
		InformerConfig{GVRString: "example.com/v1/things", GVR: thingsGVR()}, namespace, nil)
	if err != nil {
		t.Fatalf("informer: %v", err)
	}
	go inf.Run(ctx.Done())

	// Wait for the reflector to list, lose its watch, and list again.
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) && lists.Load() < 2 {
		time.Sleep(100 * time.Millisecond)
	}
	if lists.Load() < 2 {
		t.Fatalf("the reflector never re-listed (%d lists, %d watches); the test did not reach the case",
			lists.Load(), watches.Load())
	}
	cancel()
	logger.Shutdown()

	var restarts int
	for _, e := range captured(t, dir) {
		if e.EventType == "WATCH_RESTARTED" && e.GVR == "example.com/v1/things" {
			restarts++
			if e.Namespace != namespace {
				t.Errorf("namespace = %q, want %q", e.Namespace, namespace)
			}
			if e.Fields["relist"] == nil {
				t.Error("no relist count; the event must say which recovery this was")
			}
		}
	}
	if restarts == 0 {
		t.Fatalf("the watch was lost and re-listed %d times, and the capture recorded nothing - "+
			"a consumer cannot tell this from a quiet resource", lists.Load()-1)
	}
}

func thingsGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "things"}
}
