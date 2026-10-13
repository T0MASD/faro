package faro

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// The de-duplication, against a real Postgres.
//
// Not a restatement of the SQLite test: the case that only shows up here is
// both copies of one change arriving inside a SINGLE multi-row INSERT, which
// is what an overlap plus batching actually produces. The SQLite sink cannot
// reach that state - it prepares a statement per row - and ON CONFLICT DO
// NOTHING against a conflict with a row inserted by the same command is worth
// running rather than reasoning about.
//
// Scoped to a GVR unique to this run, so leftovers from an earlier run against
// the same database cannot be counted.
func TestPostgresCollapsesADuplicatedChange(t *testing.T) {
	dsn := os.Getenv("FARO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("FARO_TEST_POSTGRES_DSN not set")
	}
	gvr := fmt.Sprintf("verify.test/v1/run-%d", time.Now().UnixNano())

	ev := func(uid, rv, action string, at time.Time) Event {
		je := JSONEvent{
			EventTime: at.UTC().Format(time.RFC3339Nano),
			EventType: action, GVR: gvr, Namespace: "default",
			Name: "cm-1", UID: uid, ResourceVersion: rv,
		}
		b, _ := json.Marshal(je)
		return eventFromJSON(je, b)
	}

	s, err := newPostgresSink(dsn, "collector-a")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	// One change, seen twice 400ms apart - two collectors overlapping.
	s.Append(ev("uid-1", "500", "ADDED", at))
	s.Append(ev("uid-1", "500", "ADDED", at.Add(400*time.Millisecond)))
	// A later change, and a delete at that same version: both distinct.
	s.Append(ev("uid-1", "600", "UPDATED", at.Add(time.Second)))
	s.Append(ev("uid-1", "600", "DELETED", at.Add(2*time.Second)))
	// Four broken watches, each with no resourceVersion at all.
	for i := 0; i < 4; i++ {
		s.Append(ev("", "", "WATCH_FAILED", at.Add(time.Duration(i)*time.Second)))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err := openForTest(dsn)
	if err != nil {
		t.Fatalf("verify connect: %v", err)
	}
	defer db.Close()

	var n int
	db.QueryRow("select count(*) from events where gvr=$1 and uid='uid-1'", gvr).Scan(&n)
	if n != 3 {
		t.Errorf("the object has %d rows, want 3 - the duplicate inside one batch was not dropped", n)
	}
	db.QueryRow("select count(*) from events where gvr=$1 and action='WATCH_FAILED'", gvr).Scan(&n)
	if n != 4 {
		t.Errorf("WATCH_FAILED rows = %d, want 4 - NULL resource_versions must stay distinct", n)
	}
	var rv string
	db.QueryRow("select resource_version from events where gvr=$1 and action='DELETED'", gvr).Scan(&rv)
	if rv != "600" {
		t.Errorf("DELETED resource_version = %q, want 600 - the delete must carry the version it was deleted at", rv)
	}
}
