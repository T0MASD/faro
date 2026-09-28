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

	// What the API server currently STATES, rather than what an error code
	// implies. Populated from both informers' initial LIST and kept current by
	// their events.
	//
	// Inferring readiness from errors needs a rule per error - 404 means not
	// served, 503 means not served, anything else means proceed - and each rule
	// is a chance to classify wrong. v1.5.4 shipped with one of them missing and
	// did nothing at all. These two conditions are the API server's own answer.
	gvAvailable map[string]bool // "group/version" -> APIService Available
	crdSeen     map[string]bool // "group/resource" -> a CRD defines it
	crdEstab    map[string]bool // "group/resource" -> CRD Established
	synced      bool            // both informers have listed at least once
}

func newAPIReadiness() *apiReadiness {
	return &apiReadiness{
		waiters:     make(map[string][]chan struct{}),
		gvAvailable: make(map[string]bool),
		crdSeen:     make(map[string]bool),
		crdEstab:    make(map[string]bool),
	}
}

// markSynced is called once both readiness informers have completed their
// initial LIST. Until then the registry cannot distinguish "not ready" from
// "not seen yet", so callers fall back to asking the API directly.
func (a *apiReadiness) markSynced() {
	a.mu.Lock()
	a.synced = true
	a.mu.Unlock()
}

// state reports whether a GVR's API is usable, and whether the registry is in a
// position to say at all.
//
// Two conditions at two granularities, because they are not interchangeable:
// APIService.Available is per group/VERSION and covers whether the group is
// served, while CRD.Established is per RESOURCE - velero's two groupVersions
// carry 13 CRDs, and the group can be Available while one of them is still
// establishing, which is the datadownloads race.
func (a *apiReadiness) state(group, version, resource string) (ready, known bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.synced {
		return false, false
	}
	gvKey := group + "/" + version
	avail, seenGV := a.gvAvailable[gvKey]
	if !seenGV {
		// NO ENTRY IS NOT AN ANSWER. Every group the server serves does have an
		// APIService - measured at 32 objects for 32 groupVersions, core,
		// CRD-backed and aggregated alike - so absence usually does mean "not
		// served". But concluding that here makes this registry a single point
		// of failure for every informer in the process: a watch that fails, a
		// partial list, an endpoint that does not serve apiregistration at all,
		// and nothing ever starts. Caught by a test whose fake API server had no
		// APIServices collection: all 533 informers waited forever.
		//
		// So the registry only speaks when it has positive evidence, and
		// silence falls back to asking the API directly - which is slower and
		// always correct.
		return false, false
	}
	if !avail {
		// Positive evidence: the APIService exists and says it is not Available.
		return false, true
	}
	crdKey := group + "/" + resource
	if a.crdSeen[crdKey] && !a.crdEstab[crdKey] {
		return false, true
	}
	return true, true
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
	key := crd.Spec.Group + "/" + crd.Spec.Names.Plural
	established := false
	for _, c := range crd.Status.Conditions {
		if c.Type == apiextensionsv1.Established && c.Status == apiextensionsv1.ConditionTrue {
			established = true
			break
		}
	}
	a.mu.Lock()
	a.crdSeen[key] = true
	a.crdEstab[key] = established
	a.mu.Unlock()
	if established {
		a.ready(crd.Spec.Group)
	}
}

// onAPIService reports an aggregated API becoming Available, which is when its
// backend is serving and the aggregation layer stops answering 503.
//
// Read from the unstructured object rather than typed: the only alternative is
// to depend on k8s.io/kube-aggregator for one struct, and two string fields do
// not justify a module.
func (a *apiReadiness) onAPIService(obj *unstructured.Unstructured) {
	// spec.group is EMPTY for the core group's APIService ("v1."), which is a
	// real entry and not a malformed one: core types are the ones most likely to
	// be waited on by mistake if it is dropped.
	group, _, err := unstructured.NestedString(obj.Object, "spec", "group")
	if err != nil {
		return
	}
	conds, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return
	}
	version, _, _ := unstructured.NestedString(obj.Object, "spec", "version")
	available := false
	for _, raw := range conds {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if c["type"] == "Available" {
			available = c["status"] == "True"
			break
		}
	}
	if version != "" {
		a.mu.Lock()
		a.gvAvailable[group+"/"+version] = available
		a.mu.Unlock()
	}
	if available {
		a.ready(group)
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
