package faro

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The probe the rollout depends on must not say Ready before it is watching.
//
// The Deployment surges a second collector and waits for THIS endpoint before
// stopping the first, so the two overlap and nothing falls between them. The
// handler used to return 200 unconditionally - it answered Ready as soon as
// the port was bound - which would hand the rollout a green light while the
// new collector was still listing, and close the overlap into a gap.
func TestReadyRefusesUntilInformersHaveSynced(t *testing.T) {
	mc := &MetricsCollector{enabled: true}

	get := func() (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		mc.readinessHandler(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
		return w.Code, strings.TrimSpace(w.Body.String())
	}

	// Before the controller installs a check at all: starting up, not ready.
	if code, body := get(); code != http.StatusServiceUnavailable {
		t.Fatalf("/ready = %d (%q) before any check is installed, want 503 - a collector that has not started watching is not ready", code, body)
	}

	var started, synced int64
	mc.SetReadyCheck(func() (bool, string) {
		return started > 0 && synced >= started, "x"
	})

	// Informers created, none synced yet: still listing, still not ready.
	started, synced = 533, 349
	if code, body := get(); code != http.StatusServiceUnavailable {
		t.Fatalf("/ready = %d (%q) with 349/533 caches synced, want 503", code, body)
	}

	// Nothing started yet. "0 of 0" is vacuously complete and must NOT read as
	// ready - that is the state a collector is in before it sets anything up,
	// and it is the one that would let a rollout proceed against a collector
	// watching nothing.
	started, synced = 0, 0
	if code, _ := get(); code != http.StatusServiceUnavailable {
		t.Fatalf("/ready = %d with no informers started at all, want 503", code)
	}

	started, synced = 533, 533
	if code, body := get(); code != http.StatusOK {
		t.Fatalf("/ready = %d (%q) with every cache synced, want 200", code, body)
	}
}

// The body must say WHY, because a rollout that stalls here is a rollout
// waiting on informers, and the number of unsynced caches is the explanation.
func TestReadyReportsSyncProgress(t *testing.T) {
	mc := &MetricsCollector{enabled: true}
	mc.SetReadyCheck(func() (bool, string) { return false, "349/533 informer caches synced" })
	w := httptest.NewRecorder()
	mc.readinessHandler(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if !strings.Contains(w.Body.String(), "349/533") {
		t.Errorf("body = %q, want the sync progress - otherwise a stalled rollout says nothing about what it is waiting for", w.Body.String())
	}
}
