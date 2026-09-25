package faro

import (
	"strings"
	"sync"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// apiReadiness lets a goroutine wait for an API group to become usable, woken by
// the API server rather than by a timer.
//
// Polling a GVR every 15s was correct and lost events. An informer's initial LIST
// returns CURRENT STATE, not history: an object created and deleted inside the
// window is never seen at all, and one created then modified arrives with the
// transition already collapsed. On a cluster being built that window covers the
// addon bring-up, which is the thing the capture exists to record - 184 of 533
// informers waited on it in the last measured run.
//
// Kubernetes already publishes the exact transitions, so this subscribes to them:
//
//	CustomResourceDefinition  status.conditions[Established=True]
//	APIService                status.conditions[Available=True]
//
// Available=True is the same instant the 503s stop - measured on antrea at
// 09:56:18, to the second.
type apiReadiness struct {
	mu      sync.Mutex
	waiters map[string][]chan struct{}
}

func newAPIReadiness() *apiReadiness {
	return &apiReadiness{waiters: make(map[string][]chan struct{})}
}

// subscribe returns a channel closed when the group is reported ready. The caller
// MUST subscribe before re-checking the API, or it can miss a transition that
// happens between the check and the wait - the classic lost wakeup.
func (a *apiReadiness) subscribe(group string) chan struct{} {
	ch := make(chan struct{})
	a.mu.Lock()
	a.waiters[group] = append(a.waiters[group], ch)
	a.mu.Unlock()
	return ch
}

// unsubscribe drops a channel that is no longer waited on, so a caller that gave
// up does not leak it until the group becomes ready.
func (a *apiReadiness) unsubscribe(group string, ch chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rest := a.waiters[group][:0]
	for _, c := range a.waiters[group] {
		if c != ch {
			rest = append(rest, c)
		}
	}
	if len(rest) == 0 {
		delete(a.waiters, group)
	} else {
		a.waiters[group] = rest
	}
}

// ready wakes everything waiting on a group. Closing is idempotent per channel
// because each is removed as it is closed.
func (a *apiReadiness) ready(group string) {
	a.mu.Lock()
	chans := a.waiters[group]
	delete(a.waiters, group)
	a.mu.Unlock()
	for _, ch := range chans {
		close(ch)
	}
}

// onCRD reports a CRD becoming Established, which is when its resources can be
// listed and watched.
func (a *apiReadiness) onCRD(obj *unstructured.Unstructured) {
	var crd apiextensionsv1.CustomResourceDefinition
	if runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &crd) != nil {
		return
	}
	for _, c := range crd.Status.Conditions {
		if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
			a.ready(crd.Spec.Group)
			return
		}
	}
}

// onAPIService reports an aggregated API becoming Available, which is when its
// backend is serving and the aggregation layer stops answering 503.
//
// Read from the unstructured object rather than typed: the only alternative is
// to depend on k8s.io/kube-aggregator for one struct, and two string fields do
// not justify a module.
func (a *apiReadiness) onAPIService(obj *unstructured.Unstructured) {
	group, found, err := unstructured.NestedString(obj.Object, "spec", "group")
	if err != nil || !found || group == "" {
		return
	}
	conds, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return
	}
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if c["type"] == "Available" && c["status"] == "True" {
			a.ready(group)
			return
		}
	}
}

// groupOf takes the group from a "[group/]version/resource" GVR string. The core
// group is "", which no CRD or APIService reports, so it never waits.
func groupOf(gvrString string) string {
	parts := strings.Split(gvrString, "/")
	if len(parts) < 3 {
		return ""
	}
	return parts[0]
}
