package faro

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

type keyRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *keyRecorder) OnMatched(e MatchedEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, e.EventType+" "+e.Key)
	return nil
}

func (r *keyRecorder) has(want ...string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	got := map[string]bool{}
	for _, s := range r.seen {
		got[s] = true
	}
	for _, w := range want {
		if !got[w] {
			return false
		}
	}
	return true
}

func (r *keyRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.seen...)
	sort.Strings(out)
	return out
}

// A namespaced type watched in every namespace - namespace_names: [""] - is
// delivered, and its cache answers.
//
// That entry already built the right informer, one watch over every
// namespace, whose cache is kept under "". The worker then looked each event
// up in the cache of the OBJECT's namespace, which does not exist, so every
// event failed with "no lister found for key: v1/configmaps@a" and was
// retried forever: nothing reached a handler. A cluster-wide watch of a
// namespaced type - Warning events across the cluster, every Pod - could not
// be expressed at all.
func TestEmptyNamespaceNameWatchesEveryNamespace(t *testing.T) {
	rec := &keyRecorder{}
	dyn, c := startWithFakeAPI(t, &Config{Resources: []ResourceConfig{
		{GVR: "v1/configmaps", NamespaceNames: []string{""}},
	}}, rec)
	ctx := context.Background()
	for _, ns := range []string{"a", "b"} {
		cm := testConfigMap("x", 1)
		cm.SetNamespace(ns)
		if _, err := dyn.Resource(testConfigMaps).Namespace(ns).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if !waitFor(t, 5*time.Second, func() bool { return rec.has("ADDED a/x", "ADDED b/x") }) {
		t.Fatalf("delivered %v, want ADDED a/x and ADDED b/x", rec.all())
	}

	l, err := c.List("v1/configmaps", "")
	if err != nil || len(l) != 2 {
		t.Fatalf("List of every namespace = %d objects, err %v; want 2", len(l), err)
	}
	if got, err := c.Get("v1/configmaps", "b/x"); err != nil || got == nil || got.GetNamespace() != "b" {
		t.Fatalf("Get b/x from the every-namespace cache = %v, %v", got, err)
	}
	if got, err := c.Get("v1/configmaps", "c/x"); got != nil || err != nil {
		t.Errorf("Get of a missing object = %v, %v; want nil, nil", got, err)
	}

	if err := dyn.Resource(testConfigMaps).Namespace("a").Delete(ctx, "x", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return rec.has("DELETED a/x") }) {
		t.Fatalf("delivered %v, want DELETED a/x", rec.all())
	}
}

// Watched per namespace, an object is still read back from its own
// namespace's cache: the informer's namespace and the object's are the same,
// so carrying the informer's changes nothing there.
func TestNamedNamespacesStillReadTheirOwnCache(t *testing.T) {
	rec := &keyRecorder{}
	dyn, c := startWithFakeAPI(t, &Config{Resources: []ResourceConfig{
		{GVR: "v1/configmaps", NamespaceNames: []string{"a", "b"}},
	}}, rec)
	for _, ns := range []string{"a", "b", "c"} {
		cm := testConfigMap("x", 1)
		cm.SetNamespace(ns)
		if _, err := dyn.Resource(testConfigMaps).Namespace(ns).Create(context.Background(), cm, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if !waitFor(t, 5*time.Second, func() bool { return rec.has("ADDED a/x", "ADDED b/x") }) {
		t.Fatalf("delivered %v, want ADDED a/x and ADDED b/x", rec.all())
	}
	time.Sleep(200 * time.Millisecond)
	if rec.has("ADDED c/x") {
		t.Errorf("an object in an unwatched namespace was delivered: %v", rec.all())
	}
	if _, err := c.Get("v1/configmaps", "c/x"); err == nil {
		t.Error("Get in an unwatched namespace answered; want ErrNotWatched")
	}
}

// namespace_names [""] is also how a config asks for a cluster-scoped CRD
// that is not installed when Faro starts. Discovery does not know it, so it is
// assumed namespaced (resourceInfoFromConfig), and "" is what makes it one
// watch over the whole cluster once the CRD is Established. Reading events back
// from the informer's own cache must not change that: the informer's namespace
// and a cluster-scoped object's are both "", which is why it worked before.
func TestEmptyNamespaceNameStillWatchesAClusterScopedCRDInstalledLater(t *testing.T) {
	widgets := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	crds := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	dyn := fakedynamic.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		testConfigMaps: "ConfigMapList",
		widgets:        "WidgetList",
		crds:           "CustomResourceDefinitionList",
		{Group: "apiregistration.k8s.io", Version: "v1", Resource: "apiservices"}: "APIServiceList",
	})

	// Discovery serves example.com/v1 only once the CRD is installed. The
	// resource list is fixed up front and gated by a reactor, which the fake
	// runs under its own lock - so the install races nothing.
	var installed atomic.Bool
	disc := newFakeDiscovery()
	disc.Resources = append(disc.Resources, &metav1.APIResourceList{GroupVersion: "example.com/v1", APIResources: []metav1.APIResource{
		{Name: "widgets", Namespaced: false, Kind: "Widget", Verbs: []string{"list", "watch", "get"}}}})
	disc.PrependReactor("get", "*", func(clienttesting.Action) (bool, runtime.Object, error) {
		if installed.Load() {
			return false, nil, nil
		}
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "example.com", Resource: "widgets"}, "")
	})

	rec := &keyRecorder{}
	_, c := startOnFakeAPIWithDiscovery(t, dyn, disc, &Config{Resources: []ResourceConfig{
		{GVR: "example.com/v1/widgets", NamespaceNames: []string{""}}, // no scope: Faro cannot know it yet
	}}, rec)
	if info := c.resourceInfoFromConfig("example.com/v1/widgets"); info == nil || !info.Namespaced {
		t.Fatal("the fallback must assume namespaced here, otherwise this test proves nothing")
	}

	installed.Store(true)
	crd := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": map[string]any{"name": "widgets.example.com"},
		"spec": map[string]any{"group": "example.com", "scope": "Cluster",
			"names":    map[string]any{"plural": "widgets", "kind": "Widget"},
			"versions": []any{map[string]any{"name": "v1", "served": true, "storage": true}}},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Established", "status": "True"}}},
	}}
	if _, err := dyn.Resource(crds).Create(context.Background(), crd, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	w := &unstructured.Unstructured{}
	w.SetAPIVersion("example.com/v1")
	w.SetKind("Widget")
	w.SetName("w1")
	w.SetUID("uid-w1")
	if _, err := dyn.Resource(widgets).Create(context.Background(), w, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 10*time.Second, func() bool { return rec.has("ADDED w1") }) {
		t.Fatalf("delivered %v, want ADDED w1", rec.all())
	}
	if got, err := c.Get("example.com/v1/widgets", "w1"); err != nil || got == nil {
		t.Fatalf("Get w1 = %v, %v", got, err)
	}
}
