package e2e

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	faro "github.com/T0MASD/faro/pkg"
	"gopkg.in/yaml.v2"
	"k8s.io/apimachinery/pkg/labels"
)

// The oracle: what a config asks Faro to capture, decided by Faro's own
// configuration types rather than a copy of them.
//
// The copy this replaces had drifted from the start. Its YAML keys were
// name_pattern and namespace_patterns where Faro's are name_selector and
// namespace_names, so it read every selector as empty: it expected every
// object of a namespace-centric type, and nothing at all for a resource entry
// of a namespaced type - test 9, "should monitor resources in ALL configured
// namespaces", checked only the three Namespace objects and none of the Jobs
// and ConfigMaps it is named for. And validation checked only that expected
// events were present, so a selector Faro ignored - name_selector was never
// sent - could never show.
//
// Now the config is loaded strictly into faro.Config and normalized by
// faro.Config.Normalize, the path the binary takes, and every object in the
// manifests is decided one way or the other: captured, with its events
// required, or not, with any event for it a failure.

// manifestObject is one object the test creates - or one the cluster creates
// for it (Implicit): every namespace gets a kube-root-ca.crt ConfigMap.
type manifestObject struct {
	GVR, Namespace, Name string
	Labels               map[string]string
	Decoy                bool // annotated faro-e2e/decoy: placed so the config does NOT capture it
	Implicit             bool
	Updated              bool // changed by the update manifest
	Deleted              bool // removed by the test - directly, or with its namespace
}

func (o manifestObject) String() string {
	if o.Namespace == "" {
		return o.GVR + " " + o.Name
	}
	return o.GVR + " " + o.Namespace + "/" + o.Name
}

// expectation is what a run must show: these events, and nothing for those
// objects.
type expectation struct {
	Required  []FaroJSONEvent
	Forbidden []manifestObject
}

// decoyAnnotation marks an object placed to prove a selector excludes it.
const decoyAnnotation = "faro-e2e/decoy"

// loadFaroConfig reads a config the way the binary must understand it:
// strictly - a key Faro does not know is a config Faro does not follow - and
// normalized by Faro itself.
func loadFaroConfig(path string) (*faro.Config, map[string][]faro.NormalizedConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	cfg := &faro.Config{}
	if err := yaml.UnmarshalStrict(raw, cfg); err != nil {
		return nil, nil, fmt.Errorf("%s asks for something Faro's configuration does not define, so Faro would ignore it: %v", path, err)
	}
	norm, err := cfg.Normalize()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %v", path, err)
	}
	for gvr, ncs := range norm {
		for _, nc := range ncs {
			// A field selector is decided by the API server against fields a
			// manifest does not carry. The oracle does not model it, and says
			// so instead of guessing.
			if f := reflect.ValueOf(nc).FieldByName("FieldSelector"); f.IsValid() && f.String() != "" {
				return nil, nil, fmt.Errorf("%s: %s uses field_selector, which the e2e oracle does not model", path, gvr)
			}
		}
	}
	return cfg, norm, nil
}

// captures says whether a normalized config captures the object.
func captures(norm map[string][]faro.NormalizedConfig, o manifestObject) (bool, error) {
	for _, nc := range norm[o.GVR] {
		// A cluster-scoped object is watched cluster-wide whatever namespaces
		// the entry names. A namespaced one only in the namespaces named: an
		// entry naming none builds no informer for a namespaced type.
		if o.Namespace != "" {
			in := false
			for _, ns := range nc.NamespaceNames {
				if ns == "" || ns == o.Namespace {
					in = true
					break
				}
			}
			if !in {
				continue
			}
		}
		if nc.NameSelector != "" && nc.NameSelector != o.Name {
			continue
		}
		if nc.LabelSelector != "" {
			sel, err := labels.Parse(nc.LabelSelector)
			if err != nil {
				return false, fmt.Errorf("label_selector %q: %v", nc.LabelSelector, err)
			}
			if !sel.Matches(labels.Set(o.Labels)) {
				continue
			}
		}
		return true, nil
	}
	return false, nil
}

// manifestObjects lists every object a test's manifests create, in order:
// the create manifest's, then those only the update manifest adds; and for
// every Namespace, the kube-root-ca.crt the cluster puts in it.
func manifestObjects(createManifest, updateManifest string) ([]manifestObject, error) {
	created, err := parseManifestDocs(createManifest)
	if err != nil {
		return nil, err
	}
	var updated []manifestDoc
	if updateManifest != "" {
		if updated, err = parseManifestDocs(updateManifest); err != nil {
			return nil, err
		}
	}
	key := func(d manifestDoc) string { return d.obj.GVR + "|" + d.obj.Namespace + "|" + d.obj.Name }
	inUpdate := map[string]manifestDoc{}
	for _, d := range updated {
		inUpdate[key(d)] = d
	}
	var out []manifestObject
	seen := map[string]bool{}
	namespaces := map[string]bool{}
	add := func(d manifestDoc, changed bool) {
		o := d.obj
		o.Updated = changed
		o.Deleted = true // every test deletes what it created, namespaces with their contents
		out = append(out, o)
		seen[key(d)] = true
		if o.GVR == "v1/namespaces" {
			namespaces[o.Name] = true
		}
	}
	for _, d := range created {
		u, ok := inUpdate[key(d)]
		add(d, ok && !reflect.DeepEqual(u.raw, d.raw))
	}
	for _, d := range updated {
		if !seen[key(d)] {
			add(d, false)
		}
	}
	names := make([]string, 0, len(namespaces))
	for ns := range namespaces {
		names = append(names, ns)
	}
	sort.Strings(names)
	for _, ns := range names {
		out = append(out, manifestObject{GVR: "v1/configmaps", Namespace: ns, Name: "kube-root-ca.crt", Implicit: true})
	}
	return out, nil
}

type manifestDoc struct {
	obj manifestObject
	raw map[interface{}]interface{}
}

func parseManifestDocs(path string) ([]manifestDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []manifestDoc
	for _, doc := range strings.Split(string(data), "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var r ManifestResource
		if err := yaml.Unmarshal([]byte(doc), &r); err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		if r.Kind == "" || r.Metadata.Name == "" {
			continue // a comment-only document
		}
		var raw map[interface{}]interface{}
		if err := yaml.Unmarshal([]byte(doc), &raw); err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		out = append(out, manifestDoc{raw: raw, obj: manifestObject{
			GVR: convertToGVR(r.APIVersion, r.Kind), Namespace: r.Metadata.Namespace, Name: r.Metadata.Name,
			Labels: r.Metadata.Labels, Decoy: r.Metadata.Annotations[decoyAnnotation] == "true",
		}})
	}
	return out, nil
}

// expectFor decides every object for one test: what it must capture, and
// what it must not.
func expectFor(configFile, createManifest, updateManifest string) (expectation, error) {
	_, norm, err := loadFaroConfig(configFile)
	if err != nil {
		return expectation{}, err
	}
	objs, err := manifestObjects(createManifest, updateManifest)
	if err != nil {
		return expectation{}, err
	}
	var exp expectation
	for _, o := range objs {
		captured, err := captures(norm, o)
		if err != nil {
			return expectation{}, fmt.Errorf("%s: %v", configFile, err)
		}
		if o.Decoy && captured {
			return expectation{}, fmt.Errorf("decoy %s is captured by %s: it proves nothing about the selector it was placed for", o, configFile)
		}
		if !captured {
			exp.Forbidden = append(exp.Forbidden, o)
			continue
		}
		ev := func(t string) FaroJSONEvent {
			return FaroJSONEvent{EventType: t, GVR: o.GVR, Namespace: o.Namespace, Name: o.Name}
		}
		exp.Required = append(exp.Required, ev("ADDED"))
		if o.Implicit {
			continue // the cluster owns it: when it goes is not this test's to assert
		}
		if o.Updated {
			exp.Required = append(exp.Required, ev("UPDATED"))
		}
		if o.Deleted {
			exp.Required = append(exp.Required, ev("DELETED"))
		}
	}
	// A config that captures nothing in its manifests passes whatever Faro
	// does with the required half, so it is a broken test, not a green one.
	if len(exp.Required) == 0 {
		return expectation{}, fmt.Errorf("%s captures none of the objects in %s", configFile, createManifest)
	}
	return exp, nil
}

// handWritten keeps a test's own list of required events, checked against the
// oracle - every event names an object the config captures - with the
// oracle's forbidden objects added.
func handWritten(t *testing.T, required []FaroJSONEvent, configFile, createManifest, updateManifest string) expectation {
	t.Helper()
	exp, err := expectFor(configFile, createManifest, updateManifest)
	if err != nil {
		t.Fatalf("expectation: %v", err)
	}
	for _, r := range required {
		for _, f := range exp.Forbidden {
			if r.GVR == f.GVR && r.Namespace == f.Namespace && r.Name == f.Name {
				t.Fatalf("the hand-written expectation requires %s %s, which %s does not capture", r.EventType, f, configFile)
			}
		}
	}
	return expectation{Required: required, Forbidden: exp.Forbidden}
}

// validateExpectation checks both halves: every required event is there,
// and no event at all names a forbidden object.
func validateExpectation(t *testing.T, exp expectation, actual []FaroJSONEvent) {
	t.Helper()
	validateEvents(t, exp.Required, actual)
	for _, p := range capturedForbidden(exp, actual) {
		t.Error(p)
	}
	t.Logf("✓ checked %d objects the config must not capture", len(exp.Forbidden))
}

// capturedForbidden is the half validateEvents never had: every event that
// names an object the config does not capture.
func capturedForbidden(exp expectation, actual []FaroJSONEvent) []string {
	var out []string
	for _, f := range exp.Forbidden {
		for _, a := range actual {
			if a.GVR == f.GVR && a.Namespace == f.Namespace && a.Name == f.Name {
				why := "the config does not select it"
				if f.Decoy {
					why = "a decoy placed for exactly this"
				}
				out = append(out, fmt.Sprintf("✗ CAPTURED %s %s - %s", a.EventType, f, why))
			}
		}
	}
	return out
}

// readyURL is where a test's Faro answers whether it is watching everything
// it was configured to: the metrics server's /ready, 200 once every informer
// has been created and has synced.
func readyURL(configFile string) (string, error) {
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return "", err
	}
	cfg := &faro.Config{}
	if err := yaml.Unmarshal(raw, cfg); err != nil { // not strict: readiness must not depend on the oracle
		return "", err
	}
	if !cfg.Metrics.Enabled || cfg.Metrics.Port == 0 {
		return "", fmt.Errorf("%s does not enable the metrics server on a port of its own, so its readiness cannot be asked", configFile)
	}
	host := cfg.Metrics.BindAddr
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d/ready", host, cfg.Metrics.Port), nil
}

func askReady(url string) (bool, string) {
	resp, err := http.Get(url)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	body := make([]byte, 512)
	n, _ := resp.Body.Read(body)
	return resp.StatusCode == http.StatusOK, strings.TrimSpace(string(body[:n]))
}

// mustExpect is expectFor for a test: a config or manifest the oracle cannot
// decide is a broken test.
func mustExpect(t *testing.T, configFile, createManifest, updateManifest string) expectation {
	t.Helper()
	exp, err := expectFor(configFile, createManifest, updateManifest)
	if err != nil {
		t.Fatalf("expectation: %v", err)
	}
	return exp
}

// waitForFaroReady waits until Faro answers /ready with 200: every informer
// it was configured for created and synced.
//
// It used to wait for a log line - "started successfully", which Faro no
// longer prints because it was not true, or "Waiting for shutdown signal",
// which main prints right after Start returns, before any informer has
// listed. Manifests applied then race the informers' first list: what the test
// creates may be seen by the initial list rather than the watch, or - for a
// type whose API comes up late - not at all. /ready exists to close exactly
// that window.
func waitForFaroReady(t *testing.T, configFile string, cmd *exec.Cmd, stdout, stderr io.ReadCloser) error {
	t.Helper()
	url, err := readyURL(configFile)
	if err != nil {
		return err
	}
	fatal := make(chan string, 1)
	drain := func(r io.Reader) {
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "FARO_LOG_FILE:") || strings.Contains(line, "FARO_JSON_FILE:") {
				t.Logf("📁 %s", line)
			}
			if strings.Contains(line, "FATAL") || strings.Contains(line, "failed to start") {
				select {
				case fatal <- line:
				default:
				}
			}
		}
	}
	// Both pipes are read to the end, ready or not: a pipe nobody reads fills
	// and blocks Faro on its next log line.
	go drain(stdout)
	go drain(stderr)

	deadline := time.Now().Add(90 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		select {
		case line := <-fatal:
			return fmt.Errorf("faro startup error: %s", line)
		default:
		}
		if cmd.ProcessState != nil {
			return fmt.Errorf("faro exited before it was ready (last /ready: %s)", last)
		}
		ok, detail := askReady(url)
		if ok {
			t.Logf("Faro is ready: %s", detail)
			return nil
		}
		last = detail
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("not ready after 90s - last /ready: %s", last)
}
