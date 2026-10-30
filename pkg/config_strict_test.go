package faro

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A key faro does not know must fail the config, not vanish.
//
// yaml.Unmarshal drops unknown keys without a word. The README documented
// field_selector, which no version implemented, so a config using it loaded
// clean and watched every object of the type - the opposite of what it asked
// for, with nothing to say so.
func TestUnknownConfigKeyIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte("output_dir: /tmp\nresources:\n  - gvr: v1/pods\n    namespace_names: [a]\n    lable_selector: app=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&Config{}).LoadFromYAML(path); err == nil || !strings.Contains(err.Error(), "lable_selector") {
		t.Fatalf("a misspelt key loaded without error (err = %v)", err)
	}
}

// Every config this repository ships must still load strictly, and must not
// ask one informer for two different selectors.
func TestShippedConfigsLoadStrictly(t *testing.T) {
	files, _ := filepath.Glob("../tests/e2e/configs/*.yaml")
	files = append(files, "../examples/config-with-metrics.yaml")
	if len(files) < 2 {
		t.Fatalf("found %d config files - the test is not looking where they are", len(files))
	}
	for _, f := range files {
		c := &Config{}
		if err := c.LoadFromYAML(f); err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		checkSelectors(t, f, c)
	}

	// The operator's config lives inside its ConfigMap.
	raw, err := os.ReadFile("../deploy/operator/configmap.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(raw, &cm); err != nil {
		t.Fatal(err)
	}
	body, ok := cm.Data["config.yaml"]
	if !ok {
		t.Fatal("deploy/operator/configmap.yaml has no data.config.yaml")
	}
	c := &Config{}
	if err := yaml.UnmarshalStrict([]byte(body), c); err != nil {
		t.Errorf("operator ConfigMap config.yaml: %v", err)
	}
	checkSelectors(t, "operator ConfigMap", c)
}

func checkSelectors(t *testing.T, name string, c *Config) {
	t.Helper()
	norm, err := c.Normalize()
	if err != nil {
		return // validated elsewhere; some test configs are deliberately partial
	}
	ctrl := &Controller{logger: quietLogger(t)}
	for gvr, cfgs := range norm {
		scope := apiextensionsv1.NamespaceScoped
		for _, r := range c.Resources {
			if r.GVR == gvr && strings.EqualFold(string(r.Scope), string(ClusterScope)) {
				scope = apiextensionsv1.ClusterScoped
			}
		}
		if err := ctrl.sameSelectors(gvr, ctrl.groupConfigsByNamespace(gvr, scope, cfgs)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func quietLogger(t *testing.T) *Logger {
	t.Helper()
	l, err := NewLogger(&Config{LogLevel: "error"})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// Entries that share an informer but ask for different selectors are refused.
//
// Only the first entry's label selector was applied. A second entry for the
// same type and namespace asking for other labels - or none - silently saw
// only what the first asked for: it watched less than it was configured to,
// and the capture looked complete.
func TestDifferentSelectorsForOneInformerAreRefused(t *testing.T) {
	c := &Controller{logger: quietLogger(t)}
	groups := map[string][]NormalizedConfig{"prod": {
		{GVR: "v1/pods", LabelSelector: "app=web"},
		{GVR: "v1/pods"},
	}}
	if err := c.sameSelectors("v1/pods", groups); err == nil {
		t.Fatal("two entries asking one informer for different label selectors were accepted")
	}
	groups["prod"][1].LabelSelector = "app=web"
	if err := c.sameSelectors("v1/pods", groups); err != nil {
		t.Fatalf("identical selectors refused: %v", err)
	}

	// Several names for one informer widen to all names, as they always did
	// in effect, rather than failing a config that used to start.
	names := []NormalizedConfig{{NameSelector: "a"}, {NameSelector: "b"}}
	l, f, widened, err := informerSelectors(names)
	if err != nil || !widened || l != "" || f != "" {
		t.Errorf("differing names: %q %q widened=%t err=%v; want no selector, widened", l, f, widened, err)
	}
}

// name_selector and field_selector reach the API as a field selector.
//
// Both were accepted into the configuration and never sent: name_selector on a
// resource entry was stored and ignored, and field_selector did not exist.
func TestNameAndFieldSelectorsAreSentToTheServer(t *testing.T) {
	n := NormalizedConfig{NameSelector: "backup-job", FieldSelector: "status.phase=Running", LabelSelector: "app=x"}
	l, f, _, _ := informerSelectors([]NormalizedConfig{n})
	if l != "app=x" || f != "metadata.name=backup-job,status.phase=Running" {
		t.Fatalf("selectors = %q / %q", l, f)
	}

	c := &Controller{logger: quietLogger(t), config: &Config{}}
	var sent metav1.ListOptions
	tweak := c.listOptionsFor([]NormalizedConfig{n})
	if tweak == nil {
		t.Fatal("no list options applied for an entry with selectors")
	}
	tweak(&sent)
	if sent.FieldSelector != f || sent.LabelSelector != l {
		t.Errorf("list options = %+v", sent)
	}
}
