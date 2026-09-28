package faro

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Configurable extraction of object fields into the emitted JSON event.
//
// Faro's informers hold the whole object and the event carried only identity
// plus metadata, so a capture could say WHAT changed and never WHY: a cluster
// with 96 captured Event objects for a crash-looping pod still could not say
// that the container had exited 255, because `reason` and `message` live in
// the Event's own fields rather than in its metadata.
//
// Fields are configured PER GVR on purpose. The only reason a capture-all
// configuration is affordable is that each record is small - measured at 393
// bytes across ~5000 events - and a global "emit everything" would quietly
// remove that property. Adding reason+message to Events alone costs ~3.5%.

// deniedFieldPrefixes lists, per resource, the top-level fields that are never
// extracted whatever the configuration says.
//
// This is enforced in CODE rather than documented as a caveat. A Secret's
// `data` is base64, not encryption, so a field selector pointing at it would
// write credentials into a capture file - which in a real deployment sits on
// shared storage that every node can read, and is copied around for analysis.
// A typo in a config file must not be able to turn an observability tool into
// a credential exfiltration path.
var deniedFieldPrefixes = map[string][]string{
	"secrets": {"data", "stringData"},
}

// resourceOf returns the resource (plural) from a "group/version/resource" or
// "version/resource" GVR string.
func resourceOf(gvr string) string {
	parts := strings.Split(gvr, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// FieldDenied reports whether extracting path from this GVR is forbidden.
// Matching is on the first path segment, so "data.password" is denied for the
// same reason "data" is.
func FieldDenied(gvr, path string) bool {
	prefixes, ok := deniedFieldPrefixes[resourceOf(gvr)]
	if !ok {
		return false
	}
	head := path
	if i := strings.Index(path, "."); i >= 0 {
		head = path[:i]
	}
	for _, p := range prefixes {
		if head == p {
			return true
		}
	}
	return false
}

// extractField walks a dotted path through an unstructured object.
//
// When an intermediate value is a list, the remainder of the path is applied to
// every element and the results are returned as a list - so
// "status.containerStatuses.restartCount" yields one entry per container rather
// than failing. This is what makes a single field list usable across pods with
// differing container counts.
//
// Returns ok=false when the path is absent, so that a field list written for
// one cluster does not emit a wall of nulls on another where the type does not
// carry it.
func extractField(obj map[string]interface{}, path []string) (interface{}, bool) {
	if len(path) == 0 {
		return nil, false
	}
	cur, ok := obj[path[0]]
	if !ok || cur == nil {
		return nil, false
	}
	rest := path[1:]
	if len(rest) == 0 {
		return cur, true
	}
	switch v := cur.(type) {
	case map[string]interface{}:
		return extractField(v, rest)
	case []interface{}:
		var out []interface{}
		for _, item := range v {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if got, ok := extractField(m, rest); ok {
				out = append(out, got)
			}
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	default:
		return nil, false
	}
}

// ExtractFields returns the configured fields of obj, keyed by their path.
//
// Denied paths are skipped even if they reach here: the config loader rejects
// them too, and this is the second of the two checks on purpose - a library
// user constructing a controller directly bypasses the loader entirely.
func ExtractFields(gvr string, obj *unstructured.Unstructured, paths []string) map[string]interface{} {
	if obj == nil || len(paths) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(paths))
	for _, p := range paths {
		if p == "" || FieldDenied(gvr, p) {
			continue
		}
		if v, ok := extractField(obj.Object, strings.Split(p, ".")); ok {
			out[p] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
