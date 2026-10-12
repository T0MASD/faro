package faro

import (
	"flag"
	"os"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/klog/v2"
)

// A namespaced type that names no namespaces builds no informer.
//
// That is the contract - "all namespaces" is not expressible in this config,
// and tests/e2e asserts such an entry matches no namespaced object. The defect
// was that it happened in silence: the grouping loop below simply does not
// execute, so the config loads, Faro starts and reports healthy, and that
// resource is absent from the capture with nothing to distinguish it from a
// resource that genuinely had no events.
//
// Both halves are asserted, because the warning must not come at the cost of
// changing what gets watched.
func TestNamespacedConfigWithoutNamespacesWarns(t *testing.T) {
	l, _ := newJSONLogger(t)
	c := &Controller{logger: l}

	var buf syncBuffer
	// klog defaults to logtostderr=true, which bypasses SetOutput entirely - the
	// line goes to stderr and the buffer stays empty, so the test would pass
	// while proving nothing.
	klogInitOnce.Do(func() { klog.InitFlags(nil) })
	flag.Set("logtostderr", "false")
	klog.SetOutput(&buf)
	t.Cleanup(func() {
		klog.Flush()
		flag.Set("logtostderr", "true")
		klog.SetOutput(os.Stderr)
	})

	groups := c.groupConfigsByNamespace("v1/configmaps", apiextensionsv1.NamespaceScoped,
		[]NormalizedConfig{{GVR: "v1/configmaps"}})
	klog.Flush()

	if len(groups) != 0 {
		t.Errorf("a namespaced type naming no namespaces must build no informer, got %d group(s): %v", len(groups), groups)
	}
	if got := buf.String(); !strings.Contains(got, "names no namespaces") {
		t.Fatalf("nothing was logged for a namespaced type that will never be watched.\nklog output was:\n%s", got)
	}
}

// The two shapes that DO ask for informers must be unaffected: a cluster-scoped
// type ignores NamespaceNames and gets exactly one group, and a namespaced type
// gets one per namespace it names. Neither may warn.
func TestGroupConfigsByNamespaceLeavesValidShapesAlone(t *testing.T) {
	l, _ := newJSONLogger(t)
	c := &Controller{logger: l}

	var buf syncBuffer
	klogInitOnce.Do(func() { klog.InitFlags(nil) })
	flag.Set("logtostderr", "false")
	klog.SetOutput(&buf)
	t.Cleanup(func() {
		klog.Flush()
		flag.Set("logtostderr", "true")
		klog.SetOutput(os.Stderr)
	})

	// Cluster-scoped: NamespaceNames is ignored, one "cluster-scoped" group.
	cl := c.groupConfigsByNamespace("v1/nodes", apiextensionsv1.ClusterScoped,
		[]NormalizedConfig{{GVR: "v1/nodes"}})
	if len(cl) != 1 || len(cl["cluster-scoped"]) != 1 {
		t.Errorf("cluster-scoped grouping = %v, want one 'cluster-scoped' group", cl)
	}

	// Namespaced with names: one group per namespace.
	ns := c.groupConfigsByNamespace("v1/configmaps", apiextensionsv1.NamespaceScoped,
		[]NormalizedConfig{{GVR: "v1/configmaps", NamespaceNames: []string{"default", "kube-system"}}})
	if len(ns) != 2 || len(ns["default"]) != 1 || len(ns["kube-system"]) != 1 {
		t.Errorf("namespaced grouping = %v, want groups for default and kube-system", ns)
	}

	klog.Flush()
	if got := buf.String(); strings.Contains(got, "names no namespaces") {
		t.Fatalf("a valid config warned.\nklog output was:\n%s", got)
	}
}

// The mirror image: naming namespaces for a cluster-scoped type.
//
// Scope comes from API discovery, so the names are simply discarded and every
// object of that type is captured. The entry reads as a request to watch part
// of the cluster and quietly watches all of it - the opposite mistake to the
// one above, and just as invisible, because an author who believed the type
// was namespaced gets no other signal that it is not.
//
// The grouping must be unchanged by the warning: still exactly one
// "cluster-scoped" group, still carrying the config.
func TestClusterScopedConfigWithNamespacesWarns(t *testing.T) {
	l, _ := newJSONLogger(t)
	c := &Controller{logger: l}

	var buf syncBuffer
	klogInitOnce.Do(func() { klog.InitFlags(nil) })
	flag.Set("logtostderr", "false")
	klog.SetOutput(&buf)
	t.Cleanup(func() {
		klog.Flush()
		flag.Set("logtostderr", "true")
		klog.SetOutput(os.Stderr)
	})

	groups := c.groupConfigsByNamespace("v1/nodes", apiextensionsv1.ClusterScoped,
		[]NormalizedConfig{{GVR: "v1/nodes", NamespaceNames: []string{"default", "kube-system"}}})
	klog.Flush()

	if len(groups) != 1 || len(groups["cluster-scoped"]) != 1 {
		t.Errorf("grouping = %v, want exactly one 'cluster-scoped' group - the warning must not change what is watched", groups)
	}
	if got := buf.String(); !strings.Contains(got, "are ignored") {
		t.Fatalf("namespaces named for a cluster-scoped type were discarded with no warning.\nklog output was:\n%s", got)
	}
}
