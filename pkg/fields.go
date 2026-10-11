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

// setNested places value at path inside dst, creating intermediate maps.
//
// Paths sharing a prefix MERGE rather than replace, so asking for
// involvedObject.kind and involvedObject.name yields one involvedObject object
// carrying both.
//
// Where a prefix is already occupied by a non-map - which only happens if the
// config asks for both "status" and "status.phase" - the existing value is
// kept and the deeper path is skipped. Overwriting would mean the output
// depended on map iteration order, which is random in Go, so a capture would
// differ between runs of the same config against the same cluster.
func setNested(dst map[string]interface{}, path []string, value interface{}) {
	for i := 0; i < len(path)-1; i++ {
		next, ok := dst[path[i]]
		if !ok {
			m := make(map[string]interface{})
			dst[path[i]] = m
			dst = m
			continue
		}
		m, ok := next.(map[string]interface{})
		if !ok {
			return // a scalar already sits here; leave it alone
		}
		dst = m
	}
	leaf := path[len(path)-1]
	if _, exists := dst[leaf]; !exists {
		dst[leaf] = value
	}
}

// ExtractFields returns the configured fields of obj, as a NESTED object
// mirroring the shape of the source.
//
// Nested rather than flat keys like {"status.phase": "Running"}. A flat key
// CONTAINS a dot, and a dot is the path separator in every tool that reads
// this afterwards - so `.fields.status.phase` in jq and
// json_extract(detail,'$.status.phase') in SQLite both return null, silently,
// with no error. Worse, the failure is inconsistent: a single-segment key like
// `reason` works with dot notation, so the easy case teaches a habit that
// breaks on the first nested one. Measured on a real capture: the flat form
// returned 200 nulls where the bracket form returned 57 Pending / 131 Running.
//
// The CONFIG stays dotted - "status.containerStatuses.restartCount" is the
// natural way to NAME a path. Only the output is nested.
//
// Denied paths are skipped even if they reach here: the config loader rejects
// them too, and this is the second of the two checks on purpose - a library
// user constructing a controller directly bypasses the loader entirely.
func ExtractFields(gvr string, obj *unstructured.Unstructured, paths []string) map[string]interface{} {
	if obj == nil || len(paths) == 0 {
		return nil
	}
	out := make(map[string]interface{})
	for _, p := range paths {
		if p == "" || FieldDenied(gvr, p) {
			continue
		}
		segs := strings.Split(p, ".")
		if v, ok := extractField(obj.Object, segs); ok {
			setNested(out, segs, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
