package faro

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func sqliteEvent(name string, at time.Time) Event {
	je := JSONEvent{
		EventTime: at.UTC().Format(time.RFC3339Nano),
		EventType: "ADDED", GVR: "v1/configmaps", Namespace: "default",
		Name: name, UID: "uid-" + name,
		Labels: map[string]string{"app": "x"},
		Fields: map[string]interface{}{"reason": "Created"},
	}
	b, _ := json.Marshal(je)
	return eventFromJSON(je, b)
}

// The capture lands in the table the ANALYSIS side reads.
//
// The schema is shared with kinc_fleet's ingest-capture.py, which adds audit
// rows and kill marks to the same `events` table so one timeline holds every
// vantage point. If faro's columns drift from that, the tool stops being able
// to open what faro wrote - which is the whole point of writing SQLite.
func TestSQLiteSinkWritesTheSharedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.db")
	s, err := newSQLiteSink(path, "test-node")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := s.Append(sqliteEvent("cm-"+string(rune('a'+i)), at.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var n int
	db.QueryRow("select count(*) from events where source='faro'").Scan(&n)
	if n != 3 {
		t.Fatalf("capture holds %d rows, want 3", n)
	}

	var ts float64
	var iso, node, action, gvr, ns, name, uid, detail, labels, raw string
	err = db.QueryRow(`select ts, ts_iso, node, action, gvr, namespace, name, uid, detail, labels, json
	                   from events where name='cm-a'`).
		Scan(&ts, &iso, &node, &action, &gvr, &ns, &name, &uid, &detail, &labels, &raw)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := time.Unix(int64(ts), 0).UTC(); got.Sub(at).Abs() > time.Second {
		t.Errorf("ts = %v, want %v - the timeline is keyed on this", got, at)
	}
	for _, c := range []struct{ got, want, what string }{
		{node, "test-node", "node"}, {action, "ADDED", "action"},
		{gvr, "v1/configmaps", "gvr"}, {ns, "default", "namespace"},
		{name, "cm-a", "name"}, {uid, "uid-cm-a", "uid"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.what, c.got, c.want)
		}
	}

	// detail carries the extracted fields as JSON, which is what the analysis
	// side queries with json_extract(detail, '$.reason').
	var reason string
	if err := db.QueryRow("select json_extract(detail,'$.reason') from events where name='cm-a'").Scan(&reason); err != nil || reason != "Created" {
		t.Errorf("json_extract(detail,'$.reason') = %q (err %v), want Created", reason, err)
	}
	if labels == "" || raw == "" {
		t.Error("labels and the canonical json must both be stored - the database is meant to be a superset of the file, not a lossy copy")
	}
}

// Nothing buffered must reach the file until a flush, and everything buffered
// must reach it in ONE transaction - the cost is the commit, not the row.
func TestSQLiteSinkBatchesUntilFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.db")
	s, err := newSQLiteSink(path, "n")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := 0; i < 50; i++ {
		s.Append(sqliteEvent("x", time.Now()))
	}
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	var n int
	db.QueryRow("select count(*) from events").Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows landed before Flush; the batch is not being held", n)
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	db.QueryRow("select count(*) from events").Scan(&n)
	if n != 50 {
		t.Fatalf("after Flush the capture holds %d rows, want 50", n)
	}
}

// Reopening the same database appends to it rather than starting over. That is
// what makes a rescheduled collector continue one capture instead of leaving a
// trail of files to merge.
func TestSQLiteSinkReopensInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.db")
	for round := 0; round < 3; round++ {
		s, err := newSQLiteSink(path, "n")
		if err != nil {
			t.Fatalf("round %d open: %v", round, err)
		}
		s.Append(sqliteEvent("r", time.Now()))
		if err := s.Close(); err != nil {
			t.Fatalf("round %d close: %v", round, err)
		}
	}
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	var n int
	db.QueryRow("select count(*) from events").Scan(&n)
	if n != 3 {
		t.Fatalf("after three opens the capture holds %d rows, want 3 - each restart must continue the same capture", n)
	}
}
