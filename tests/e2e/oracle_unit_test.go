package e2e

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The oracle decides what every e2e test asserts, so it is tested itself,
// without a cluster: for each config and its manifests, exactly these events
// are required and exactly these objects are forbidden. Each line was checked
// by hand against the config's selectors.
//
// Expected red on main for tests 1, 3, 6 and 7: their namespace entries use
// name_selector, which main's ResourceDetails does not define - main loads
// those configs and ignores the selector. Green once fix/config-and-ci is
// merged.
func TestOracleExpectations(t *testing.T) {
	cases := map[int]struct{ required, forbidden string }{
		1: {`ADDED v1/configmaps faro-test-1/test-config-1
UPDATED v1/configmaps faro-test-1/test-config-1
DELETED v1/configmaps faro-test-1/test-config-1`, `v1/namespaces faro-test-1
v1/configmaps faro-test-1/decoy-wrong-name
v1/secrets faro-test-1/test-secret-not-monitored
v1/services faro-test-1/test-service-not-monitored
v1/configmaps faro-test-1/kube-root-ca.crt`},
		2: {`ADDED v1/configmaps faro-test-2/test-config-1
UPDATED v1/configmaps faro-test-2/test-config-1
DELETED v1/configmaps faro-test-2/test-config-1`, `v1/namespaces faro-test-2
v1/configmaps faro-test-2/decoy-wrong-name
v1/configmaps faro-test-2/unmonitored-config
v1/secrets faro-test-2/test-secret-background
v1/configmaps faro-test-2/kube-root-ca.crt`},
		3: {`ADDED v1/configmaps faro-test-3/test-config-1
UPDATED v1/configmaps faro-test-3/test-config-1
DELETED v1/configmaps faro-test-3/test-config-1`, `v1/namespaces faro-test-3
v1/configmaps faro-test-3/decoy-wrong-name
v1/configmaps faro-test-3/decoy-wrong-label
v1/configmaps faro-test-3/kube-root-ca.crt`},
		4: {`ADDED v1/configmaps faro-test-4/test-config-1
UPDATED v1/configmaps faro-test-4/test-config-1
DELETED v1/configmaps faro-test-4/test-config-1`, `v1/namespaces faro-test-4
v1/configmaps faro-test-4/decoy-wrong-name
v1/configmaps faro-test-4/decoy-wrong-label
v1/configmaps faro-test-4/kube-root-ca.crt`},
		5: {`ADDED v1/namespaces faro-test-5
UPDATED v1/namespaces faro-test-5
DELETED v1/namespaces faro-test-5`, `v1/namespaces faro-test-5-decoy
v1/configmaps faro-test-5/kube-root-ca.crt
v1/configmaps faro-test-5-decoy/kube-root-ca.crt`},
		6: {`ADDED v1/namespaces faro-test-6
DELETED v1/namespaces faro-test-6
ADDED v1/configmaps faro-test-6/test-config-1
UPDATED v1/configmaps faro-test-6/test-config-1
DELETED v1/configmaps faro-test-6/test-config-1`, `v1/configmaps faro-test-6/decoy-wrong-name
v1/namespaces faro-test-6-decoy
v1/configmaps faro-test-6/kube-root-ca.crt
v1/configmaps faro-test-6-decoy/kube-root-ca.crt`},
		7: {`ADDED v1/configmaps faro-test-7/test-config-1
UPDATED v1/configmaps faro-test-7/test-config-1
DELETED v1/configmaps faro-test-7/test-config-1`, `v1/namespaces faro-test-7
v1/configmaps faro-test-7/test-config-2
v1/configmaps faro-test-7/decoy-wrong-name
v1/configmaps faro-test-7/additional-config-not-monitored
v1/secrets faro-test-7/test7-background-secret
v1/services faro-test-7/test7-background-service
v1/configmaps faro-test-7/kube-root-ca.crt`},
		8: {`ADDED v1/namespaces faro-test-8a
UPDATED v1/namespaces faro-test-8a
DELETED v1/namespaces faro-test-8a
ADDED v1/namespaces faro-test-8b
UPDATED v1/namespaces faro-test-8b
DELETED v1/namespaces faro-test-8b
ADDED v1/namespaces faro-test-8c
DELETED v1/namespaces faro-test-8c`, `v1/namespaces faro-test-8-decoy
v1/configmaps faro-test-8-decoy/kube-root-ca.crt
v1/configmaps faro-test-8a/kube-root-ca.crt
v1/configmaps faro-test-8b/kube-root-ca.crt
v1/configmaps faro-test-8c/kube-root-ca.crt`},
		// Test 9 is the case the old oracle could not see: the Jobs and
		// ConfigMaps in all three namespaces are required now, not only the
		// Namespace objects.
		9: {`ADDED v1/namespaces faro-multi-main
UPDATED v1/namespaces faro-multi-main
DELETED v1/namespaces faro-multi-main
ADDED v1/namespaces faro-multi-app
UPDATED v1/namespaces faro-multi-app
DELETED v1/namespaces faro-multi-app
ADDED v1/namespaces faro-multi-db
UPDATED v1/namespaces faro-multi-db
DELETED v1/namespaces faro-multi-db
ADDED batch/v1/jobs faro-multi-main/main-job
DELETED batch/v1/jobs faro-multi-main/main-job
ADDED v1/configmaps faro-multi-main/main-config
UPDATED v1/configmaps faro-multi-main/main-config
DELETED v1/configmaps faro-multi-main/main-config
ADDED batch/v1/jobs faro-multi-app/app-job
DELETED batch/v1/jobs faro-multi-app/app-job
ADDED v1/configmaps faro-multi-app/app-config
UPDATED v1/configmaps faro-multi-app/app-config
DELETED v1/configmaps faro-multi-app/app-config
ADDED batch/v1/jobs faro-multi-db/db-job
DELETED batch/v1/jobs faro-multi-db/db-job
ADDED v1/configmaps faro-multi-db/db-config
UPDATED v1/configmaps faro-multi-db/db-config
DELETED v1/configmaps faro-multi-db/db-config
ADDED v1/configmaps faro-multi-app/kube-root-ca.crt
ADDED v1/configmaps faro-multi-db/kube-root-ca.crt
ADDED v1/configmaps faro-multi-main/kube-root-ca.crt`, `v1/secrets faro-multi-main/decoy-wrong-type
v1/configmaps default/decoy-other-namespace`},
		10: {`ADDED v1/configmaps faro-test-10/test-config-10
UPDATED v1/configmaps faro-test-10/test-config-10
DELETED v1/configmaps faro-test-10/test-config-10
ADDED v1/secrets faro-test-10/test-secret-10
DELETED v1/secrets faro-test-10/test-secret-10`, `v1/namespaces faro-test-10
v1/configmaps faro-test-10/decoy-wrong-name
v1/configmaps faro-test-10/kube-root-ca.crt`},
	}
	for n := 1; n <= 10; n++ {
		want := cases[n]
		t.Run(fmt.Sprintf("test%d", n), func(t *testing.T) {
			exp, err := expectFor(fmt.Sprintf("configs/simple-test-%d.yaml", n),
				fmt.Sprintf("manifests/test%d-manifest.yaml", n), fmt.Sprintf("manifests/test%d-manifest-update.yaml", n))
			if err != nil {
				t.Fatal(err)
			}
			var req, forb []string
			for _, r := range exp.Required {
				o := manifestObject{GVR: r.GVR, Namespace: r.Namespace, Name: r.Name}
				req = append(req, r.EventType+" "+o.String())
			}
			for _, f := range exp.Forbidden {
				forb = append(forb, f.String())
			}
			if got, w := sorted(req), sorted(lines(want.required)); !reflect.DeepEqual(got, w) {
				t.Errorf("required:\n got  %v\n want %v", got, w)
			}
			if got, w := sorted(forb), sorted(lines(want.forbidden)); !reflect.DeepEqual(got, w) {
				t.Errorf("forbidden:\n got  %v\n want %v", got, w)
			}
		})
	}
}

// Validation fails both ways: a required event missing, and any event for a
// forbidden object - the half the harness never checked.
func TestValidationRefusesAForbiddenCapture(t *testing.T) {
	exp := expectation{
		Required:  []FaroJSONEvent{{EventType: "ADDED", GVR: "v1/configmaps", Namespace: "ns", Name: "wanted"}},
		Forbidden: []manifestObject{{GVR: "v1/configmaps", Namespace: "ns", Name: "decoy", Decoy: true}},
	}
	clean := []FaroJSONEvent{{EventType: "ADDED", GVR: "v1/configmaps", Namespace: "ns", Name: "wanted"}}
	if p := capturedForbidden(exp, clean); len(p) != 0 {
		t.Errorf("a clean capture reported %v", p)
	}
	leaked := append(clean, FaroJSONEvent{EventType: "UPDATED", GVR: "v1/configmaps", Namespace: "ns", Name: "decoy"})
	if p := capturedForbidden(exp, leaked); len(p) != 1 || !strings.Contains(p[0], "decoy") {
		t.Errorf("a captured decoy reported %v, want one problem naming it", p)
	}
}

// A decoy the config would capture proves nothing, and a config that asks for
// a key Faro does not define would be followed only in part: both are broken
// tests, reported as such rather than run.
func TestOracleRefusesBrokenTests(t *testing.T) {
	if _, err := expectFor("configs/simple-test-2.yaml", "testdata/decoy-captured.yaml", ""); err == nil ||
		!strings.Contains(err.Error(), "decoy") {
		t.Errorf("a decoy the config captures: err = %v", err)
	}
	if _, err := expectFor("testdata/unknown-key.yaml", "manifests/test2-manifest.yaml", ""); err == nil ||
		!strings.Contains(err.Error(), "does not define") {
		t.Errorf("a config key Faro does not define: err = %v", err)
	}
}

func lines(s string) []string { return strings.Split(strings.TrimSpace(s), "\n") }

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}
