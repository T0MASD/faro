package faro

import (
	"encoding/json"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func obj(m map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: m}
}

// The case this feature exists for: a capture held 96 Event objects for a
// crash-looping pod and could not say why, because reason and message are the
// Event's own fields rather than metadata.
func TestExtractFieldsEventReasonAndMessage(t *testing.T) {
	e := obj(map[string]interface{}{
		"reason":  "BackOff",
		"message": "Back-off restarting failed container",
		"type":    "Warning",
		"involvedObject": map[string]interface{}{
			"kind": "Pod",
			"name": "antrea-agent-86jgg",
		},
	})
	got := ExtractFields("v1/events", e,
		[]string{"reason", "message", "involvedObject.name"})
	want := map[string]interface{}{
		"reason":              "BackOff",
		"message":             "Back-off restarting failed container",
		"involvedObject.name": "antrea-agent-86jgg",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// A path crossing a list applies to every element, so one field list works
// across pods with differing container counts.
func TestExtractFieldsTraversesLists(t *testing.T) {
	p := obj(map[string]interface{}{
		"status": map[string]interface{}{
			"containerStatuses": []interface{}{
				map[string]interface{}{"name": "antrea-agent", "restartCount": int64(2)},
				map[string]interface{}{"name": "antrea-ovs", "restartCount": int64(0)},
			},
		},
	})
	got := ExtractFields("v1/pods", p, []string{"status.containerStatuses.restartCount"})
	want := []interface{}{int64(2), int64(0)}
	if !reflect.DeepEqual(got["status.containerStatuses.restartCount"], want) {
		t.Fatalf("got %#v, want %#v", got["status.containerStatuses.restartCount"], want)
	}
}

// Absent paths are omitted, not emitted as null: a field list written for one
// cluster must not produce a wall of nulls on another.
func TestExtractFieldsOmitsAbsent(t *testing.T) {
	got := ExtractFields("v1/events", obj(map[string]interface{}{"reason": "Started"}),
		[]string{"reason", "message", "status.phase"})
	if _, ok := got["message"]; ok {
		t.Fatal("absent path should be omitted")
	}
	if len(got) != 1 {
		t.Fatalf("expected only the present path, got %#v", got)
	}
}

// Nothing configured means no Fields key at all, so a capture taken without
// this feature is byte-identical to one from before it existed.
func TestExtractFieldsNilWhenNothingConfigured(t *testing.T) {
	if got := ExtractFields("v1/events", obj(map[string]interface{}{"reason": "x"}), nil); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
	if got := ExtractFields("v1/events", obj(map[string]interface{}{}), []string{"reason"}); got != nil {
		t.Fatalf("expected nil when nothing matched, got %#v", got)
	}
	ev := JSONEvent{EventType: "ADDED", GVR: "v1/events", Name: "x"}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"eventTime":"","eventType":"ADDED","gvr":"v1/events","name":"x"}` {
		t.Fatalf("Fields must not appear when empty: %s", b)
	}
}

// A Secret's data is base64, not encryption. A field selector pointing at it
// would write credentials into a capture file that lives on shared storage, so
// this is refused in code whatever the config says.
func TestSecretDataIsNeverExtracted(t *testing.T) {
	s := obj(map[string]interface{}{
		"type": "Opaque",
		"data": map[string]interface{}{"password": "aHVudGVyMg=="},
	})
	got := ExtractFields("v1/secrets", s, []string{"data", "data.password", "type"})
	if _, ok := got["data"]; ok {
		t.Fatal("secret data must never be extracted")
	}
	if _, ok := got["data.password"]; ok {
		t.Fatal("secret data subpath must never be extracted")
	}
	if got["type"] != "Opaque" {
		t.Fatalf("non-sensitive fields should still work, got %#v", got)
	}
}

func TestFieldDenied(t *testing.T) {
	for _, tc := range []struct {
		gvr, path string
		want      bool
	}{
		{"v1/secrets", "data", true},
		{"v1/secrets", "data.password", true},
		{"v1/secrets", "stringData", true},
		{"v1/secrets", "type", false},
		{"v1/configmaps", "data", false}, // not sensitive by default
		{"v1/events", "reason", false},
		{"apps/v1/deployments", "data", false},
	} {
		if got := FieldDenied(tc.gvr, tc.path); got != tc.want {
			t.Errorf("FieldDenied(%q,%q)=%v want %v", tc.gvr, tc.path, got, tc.want)
		}
	}
}

// The denylist is enforced at config load as well as on the emit path - two
// checks, because a library user can build a controller without Normalize.
func TestNormalizeDropsDeniedFields(t *testing.T) {
	c := &Config{Resources: []ResourceConfig{
		{GVR: "v1/secrets", Fields: []string{"data", "type"}},
		{GVR: "v1/events", Fields: []string{"reason", "message"}},
	}}
	n, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got := n["v1/secrets"][0].Fields; !reflect.DeepEqual(got, []string{"type"}) {
		t.Fatalf("secrets fields = %#v, want [type]", got)
	}
	if got := n["v1/events"][0].Fields; !reflect.DeepEqual(got, []string{"reason", "message"}) {
		t.Fatalf("events fields = %#v", got)
	}
}

func TestNormalizeCarriesFieldsFromNamespaceFormat(t *testing.T) {
	c := &Config{Namespaces: []NamespaceConfig{{
		NameSelector: "kube-system",
		Resources: map[string]ResourceDetails{
			"v1/events": {Fields: []string{"reason"}},
		},
	}}}
	n, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got := n["v1/events"][0].Fields; !reflect.DeepEqual(got, []string{"reason"}) {
		t.Fatalf("got %#v", got)
	}
}
