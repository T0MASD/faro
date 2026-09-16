package integration

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	faro "github.com/T0MASD/faro/pkg"
	"github.com/T0MASD/faro/tests/testutils"
)

// Faro is meant to be pointed at a GVR before the type exists: start it, install the CRD
// later, and the objects that appear are captured. This is the whole test - it configures a
// GVR with no such CRD in the cluster, starts Faro, and only then installs the type.
//
// Before the change this fails at the informer count: the GVR is absent from discovery
// results, so it is skipped, no informer is started, and the object that arrives afterwards
// is never seen.

const (
	lateNamespace = "faro-late-crd"
	lateGVR       = "faro.test/v1alpha1/lateprobes"
	lateCRD       = "lateprobes.faro.test"
	lateObject    = "arrived-later"
)

type lateEventCollector struct {
	events chan faro.MatchedEvent
}

func (l *lateEventCollector) OnMatched(event faro.MatchedEvent) error {
	select {
	case l.events <- event:
	default:
	}
	return nil
}

func kubectlWithInput(t *testing.T, manifest string, args ...string) {
	t.Helper()
	cmd := exec.Command("kubectl", args...)
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestWatchesResourceInstalledAfterStart(t *testing.T) {
	logDir := "./logs/TestWatchesResourceInstalledAfterStart"
	testutils.EnsureLogDir(t, logDir)

	// Start from a cluster that does not serve the type at all.
	exec.Command("kubectl", "delete", "crd", lateCRD, "--ignore-not-found").Run()
	exec.Command("kubectl", "delete", "namespace", lateNamespace, "--ignore-not-found").Run()
	defer func() {
		exec.Command("kubectl", "delete", "crd", lateCRD, "--ignore-not-found").Run()
		exec.Command("kubectl", "delete", "namespace", lateNamespace, "--ignore-not-found").Run()
	}()
	if out, err := exec.Command("kubectl", "create", "namespace", lateNamespace).CombinedOutput(); err != nil {
		t.Fatalf("create namespace: %v\n%s", err, out)
	}

	config := &faro.Config{
		OutputDir:  logDir,
		LogLevel:   "info",
		JsonExport: true,
		Resources: []faro.ResourceConfig{{
			GVR:            lateGVR,
			Scope:          faro.NamespaceScope,
			NamespaceNames: []string{lateNamespace},
		}},
	}
	client, err := faro.NewKubernetesClient()
	if err != nil {
		t.Fatalf("kubernetes client: %v", err)
	}
	logger, err := faro.NewLogger(config)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	defer logger.Shutdown()

	controller := faro.NewController(client, logger, config)
	collector := &lateEventCollector{events: make(chan faro.MatchedEvent, 16)}
	controller.AddEventHandler(collector)

	if err := controller.Start(); err != nil {
		t.Fatalf("controller did not start for a type the cluster does not serve yet: %v", err)
	}
	defer controller.Stop()

	if builtin, _ := controller.GetActiveInformers(); builtin != 1 {
		t.Fatalf("started %d config-driven informers for %s, want 1: the type is not installed yet, "+
			"which is exactly the case this is meant to support", builtin, lateGVR)
	}

	t.Log("installing the CRD after Faro is already watching")
	kubectlWithInput(t, `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: `+lateCRD+`
spec:
  group: faro.test
  scope: Namespaced
  names:
    plural: lateprobes
    singular: lateprobe
    kind: LateProbe
  versions:
    - name: v1alpha1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                track: {type: string}
`, "apply", "-f", "-")
	if out, err := exec.Command("kubectl", "wait", "--for=condition=Established",
		"crd/"+lateCRD, "--timeout=60s").CombinedOutput(); err != nil {
		t.Fatalf("CRD never established: %v\n%s", err, out)
	}

	kubectlWithInput(t, `
apiVersion: faro.test/v1alpha1
kind: LateProbe
metadata:
  name: `+lateObject+`
  namespace: `+lateNamespace+`
spec:
  track: stable
`, "apply", "-f", "-")

	deadline := time.After(90 * time.Second)
	for {
		select {
		case event := <-collector.events:
			if event.GVR == lateGVR && event.Object.GetName() == lateObject {
				t.Logf("captured %s %s/%s", event.EventType, event.Object.GetNamespace(), event.Object.GetName())
				return
			}
		case <-deadline:
			t.Fatalf("%s was created after the CRD was installed and Faro never saw it", lateObject)
		}
	}
}
