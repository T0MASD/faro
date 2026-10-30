package faro

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

var testConfigMaps = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

// startWithFakeAPI runs a whole controller, informers included, against
// client-go's fake dynamic client: the path a library consumer takes, with no
// cluster. Every handler given is registered before Start.
func startWithFakeAPI(t *testing.T, cfg *Config, handlers ...EventHandler) (*fakedynamic.FakeDynamicClient, *Controller) {
	t.Helper()
	return startOnFakeAPI(t, newFakeAPI(), cfg, handlers...)
}

// newFakeAPI is the fake API alone, for a test that installs reactors - a list
// that hangs, a watch that expires - before the controller starts.
func newFakeAPI() *fakedynamic.FakeDynamicClient {
	return fakedynamic.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		testConfigMaps: "ConfigMapList",
		{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}: "CustomResourceDefinitionList",
		{Group: "apiregistration.k8s.io", Version: "v1", Resource: "apiservices"}:             "APIServiceList",
	})
}

func startOnFakeAPI(t *testing.T, dyn *fakedynamic.FakeDynamicClient, cfg *Config, handlers ...EventHandler) (*fakedynamic.FakeDynamicClient, *Controller) {
	t.Helper()
	return startOnFakeAPIWithDiscovery(t, dyn, newFakeDiscovery(), cfg, handlers...)
}

// newFakeDiscovery serves configmaps in v1, and nothing else.
func newFakeDiscovery() *fakediscovery.FakeDiscovery {
	disc := &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{}}
	disc.Resources = []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{
		{Name: "configmaps", Namespaced: true, Kind: "ConfigMap", Verbs: []string{"list", "watch", "get"}}}}}
	return disc
}

func startOnFakeAPIWithDiscovery(t *testing.T, dyn *fakedynamic.FakeDynamicClient, disc *fakediscovery.FakeDiscovery, cfg *Config, handlers ...EventHandler) (*fakedynamic.FakeDynamicClient, *Controller) {
	t.Helper()
	if cfg.OutputDir == "" {
		cfg.OutputDir = t.TempDir()
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "warning"
	}
	if len(cfg.Resources) == 0 {
		cfg.Resources = []ResourceConfig{{GVR: "v1/configmaps", NamespaceNames: []string{"t"}}}
	}
	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := NewController(&KubernetesClient{Dynamic: dyn, Discovery: disc, Config: &rest.Config{}}, logger, cfg)
	for _, h := range handlers {
		c.AddEventHandler(h)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Stop(); logger.Shutdown() })
	deadline := time.Now().Add(10 * time.Second)
	for !c.metricsReady() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return dyn, c
}

// metricsReady is /ready's answer: every informer created and synced.
func (c *Controller) metricsReady() bool {
	ok, _ := c.readyForCapture()
	return ok
}

func testConfigMap(name string, seq int) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("v1")
	u.SetKind("ConfigMap")
	u.SetNamespace("t")
	u.SetName(name)
	u.SetUID(types.UID("uid-" + name))
	u.SetResourceVersion(strconv.Itoa(seq + 1))
	u.SetLabels(map[string]string{"app": "inbox"})
	u.SetAnnotations(map[string]string{"seq": strconv.Itoa(seq)})
	return u
}

func seqOf(o *unstructured.Unstructured) int {
	s, _ := strconv.Atoi(o.GetAnnotations()["seq"])
	return s
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// orderRecorder is a handler that takes as long as a POST to a downstream
// service does, and records what it was handed per object - and whether it was
// ever handed two events for one object at the same time.
type orderRecorder struct {
	mu         sync.Mutex
	seen       map[string][]int
	inFlight   map[string]bool
	concurrent int
	calls      atomic.Int64
}

func (r *orderRecorder) OnMatched(e MatchedEvent) error {
	name := e.Object.GetName()
	r.mu.Lock()
	if r.inFlight[name] {
		r.concurrent++
	}
	r.inFlight[name] = true
	r.mu.Unlock()

	time.Sleep(time.Duration(1+rand.Intn(5)) * time.Millisecond)

	r.mu.Lock()
	r.seen[name] = append(r.seen[name], seqOf(e.Object))
	r.inFlight[name] = false
	r.mu.Unlock()
	r.calls.Add(1)
	return nil
}

// Events for one object must reach a handler one at a time, in the order they
// were observed.
//
// They did not. Each event was queued as its own work item, so up to three
// workers held events for one object at once, and every handler call ran on a
// goroutine of its own, so even a single worker raced itself. Measured with a
// handler that sleeps 1-5ms, as a POST to a downstream service does: 20
// objects updated 30 times each reached the handler with 0, 6, 22, 2, 15 and
// 14 pairs inverted over six runs. A consumer that reconciles from these
// events - the reason to use this as a library - can end on a stale state.
func TestEventsForOneObjectAreDeliveredInOrder(t *testing.T) {
	const objects, updates = 20, 30
	h := &orderRecorder{seen: map[string][]int{}, inFlight: map[string]bool{}}
	dyn, _ := startWithFakeAPI(t, &Config{}, h)
	ctx := context.Background()
	cms := dyn.Resource(testConfigMaps).Namespace("t")

	for o := 0; o < objects; o++ {
		if _, err := cms.Create(ctx, testConfigMap(fmt.Sprintf("run-%d", o), 0), metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for s := 1; s <= updates; s++ {
		for o := 0; o < objects; o++ {
			if _, err := cms.Update(ctx, testConfigMap(fmt.Sprintf("run-%d", o), s), metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
	}

	want := int64(objects * (updates + 1))
	if !waitFor(t, 30*time.Second, func() bool { return h.calls.Load() >= want }) {
		t.Fatalf("handler called %d times, want %d - events were lost", h.calls.Load(), want)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	inversions := 0
	for name, seq := range h.seen {
		for i := 1; i < len(seq); i++ {
			if seq[i] < seq[i-1] {
				inversions++
			}
		}
		if last := seq[len(seq)-1]; last != updates {
			t.Errorf("%s: the handler's last view is seq %d, the object is at %d", name, last, updates)
		}
	}
	if inversions > 0 {
		t.Errorf("%d pairs of events for one object reached the handler out of order", inversions)
	}
	if h.concurrent > 0 {
		t.Errorf("the handler was given two events for one object at once, %d times", h.concurrent)
	}
}

// failingOnce errors the first time it is called, then records.
type failingOnce struct {
	calls atomic.Int64
	mu    sync.Mutex
	got   []string
}

func (f *failingOnce) OnMatched(e MatchedEvent) error {
	if f.calls.Add(1) == 1 {
		return fmt.Errorf("downstream unavailable")
	}
	f.mu.Lock()
	f.got = append(f.got, e.EventType+" "+e.Object.GetName())
	f.mu.Unlock()
	return nil
}

func (f *failingOnce) delivered() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

// countingHandler records how many times it was handed each event type.
type countingHandler struct {
	mu sync.Mutex
	n  map[string]int
}

func (h *countingHandler) OnMatched(e MatchedEvent) error {
	h.mu.Lock()
	h.n[e.EventType]++
	h.mu.Unlock()
	return nil
}

func (h *countingHandler) count(t string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.n[t]
}

// A handler that returns an error must be handed the event again.
//
// The error was logged as a Warning and the event dropped: one call, never
// redelivered. For a consumer whose downstream blinks - a restart, a 503 - the
// change is simply gone, and nothing re-sends it until the object changes
// again.
//
// The retry resumes at the handler that failed: one that accepted the event
// before it is not handed it twice, and the capture records it once.
func TestHandlerErrorRedeliversTheEvent(t *testing.T) {
	flaky := &failingOnce{}
	before := &countingHandler{n: map[string]int{}} // registered first, accepts every event
	dyn, _ := startWithFakeAPI(t, &Config{}, before, flaky)
	if _, err := dyn.Resource(testConfigMaps).Namespace("t").Create(context.Background(), testConfigMap("once", 7), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 10*time.Second, func() bool { return len(flaky.delivered()) > 0 }) {
		t.Fatalf("the handler failed once (calls: %d) and the event was never handed to it again", flaky.calls.Load())
	}
	if got := flaky.delivered(); len(got) != 1 || got[0] != "ADDED once" {
		t.Errorf("redelivered %v, want [ADDED once]", got)
	}
	if n := before.count("ADDED"); n != 1 {
		t.Errorf("the handler ahead of the failing one was handed the event %d times, want 1", n)
	}
}

// A handler that panics must not take the collector down; it is an error, and
// retried as one.
func TestHandlerPanicIsAnError(t *testing.T) {
	ev := MatchedEvent{EventType: "ADDED", Object: testConfigMap("p", 0)}
	err := callHandler(panicking{}, ev)
	if err == nil {
		t.Fatal("a panicking handler returned no error")
	}
}

type panicking struct{}

func (panicking) OnMatched(MatchedEvent) error { panic("boom") }

// A handler that never succeeds holds its object's later events only until the
// retries run out; then it is given up on and the next event is delivered.
func TestHandlerThatAlwaysFailsIsGivenUpOn(t *testing.T) {
	h := &alwaysFailsAdded{}
	dyn, _ := startWithFakeAPI(t, &Config{}, h)
	cms := dyn.Resource(testConfigMaps).Namespace("t")
	ctx := context.Background()
	if _, err := cms.Create(ctx, testConfigMap("stuck", 0), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := cms.Update(ctx, testConfigMap("stuck", 1), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 15*time.Second, func() bool { return h.ok.Load() > 0 }) {
		t.Fatalf("after %d failed attempts the next event was never delivered", h.failed.Load())
	}
	if f := h.failed.Load(); f != maxDeliveryRetries+1 {
		t.Errorf("failed attempts = %d, want %d (the first try and %d retries)", f, maxDeliveryRetries+1, maxDeliveryRetries)
	}
}

// alwaysFailsAdded fails every delivery of an ADDED, and accepts the rest.
type alwaysFailsAdded struct {
	failed, ok atomic.Int64
}

func (a *alwaysFailsAdded) OnMatched(e MatchedEvent) error {
	if e.EventType == "ADDED" {
		a.failed.Add(1)
		return fmt.Errorf("never")
	}
	a.ok.Add(1)
	return nil
}

// A handler that panics under a running controller is an error like any other:
// the collector stays up, and the object's next event is still delivered.
//
// Every handler call ran on a goroutine of its own, outside any recover, so one
// panicking handler took the whole process down - the capture with it.
func TestPanickingHandlerDoesNotStopTheCollector(t *testing.T) {
	h := &panicsOnFirstAdded{}
	dyn, _ := startWithFakeAPI(t, &Config{}, h)
	cms := dyn.Resource(testConfigMaps).Namespace("t")
	ctx := context.Background()
	if _, err := cms.Create(ctx, testConfigMap("p", 0), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := cms.Update(ctx, testConfigMap("p", 1), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 10*time.Second, func() bool { return h.updated.Load() > 0 }) {
		t.Fatalf("after a handler panicked (%d times) the object's next event was never delivered", h.panics.Load())
	}
}

type panicsOnFirstAdded struct{ panics, updated atomic.Int64 }

func (p *panicsOnFirstAdded) OnMatched(e MatchedEvent) error {
	if e.EventType == "ADDED" && p.panics.Add(1) == 1 {
		panic("boom")
	}
	if e.EventType == "UPDATED" {
		p.updated.Add(1)
	}
	return nil
}

// A retry hands the event to the handler again, not to the capture: the record
// of a change must not depend on whether some consumer's downstream blinked.
func TestRetriedEventIsCapturedOnce(t *testing.T) {
	cfg := &Config{JsonExport: true}
	flaky := &failingOnce{}
	dyn, c := startWithFakeAPI(t, cfg, flaky)
	if _, err := dyn.Resource(testConfigMaps).Namespace("t").Create(context.Background(), testConfigMap("once", 3), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 10*time.Second, func() bool { return len(flaky.delivered()) > 0 }) {
		t.Fatal("the event was never redelivered")
	}
	c.logger.FlushCapture()
	n := 0
	for _, e := range captured(t, cfg.OutputDir) {
		if e.EventType == "ADDED" && e.Name == "once" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the capture holds %d ADDED records for one change delivered twice, want 1", n)
	}
}

// workers: in the config sets how many objects are worked on at once.
func TestWorkersAreConfigurable(t *testing.T) {
	cfg := &Config{OutputDir: t.TempDir(), LogLevel: "warning"}
	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Shutdown()
	if n := NewController(&KubernetesClient{}, logger, cfg).workers; n != 3 {
		t.Errorf("default workers = %d, want 3", n)
	}
	cfg.Workers = 8
	if n := NewController(&KubernetesClient{}, logger, cfg).workers; n != 8 {
		t.Errorf("workers: 8 gave %d", n)
	}
}
