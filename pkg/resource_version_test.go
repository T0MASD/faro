package faro

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	_ "modernc.org/sqlite"
)

func captured(t *testing.T, dir string) []JSONEvent {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "logs", "events-*.json"))
	var out []JSONEvent
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var e JSONEvent
			if err := json.Unmarshal([]byte(line), &e); err == nil {
				out = append(out, e)
			}
		}
	}
	return out
}

// Every observed event must carry the resourceVersion of the CHANGE.
//
// eventTime is the observer's - two collectors watching one stream stamp one
// change with two different times, so nothing downstream can tell "both saw
// this" from "it happened twice". resourceVersion is the change's own, so it
// can.
//
// The two paths reach it differently and both are asserted here, because the
// delete path is the one that can silently lose it: reconcile rebuilds a stub
// for an object that is already gone and has nothing left to read a
// resourceVersion off, so the handler must have captured it - exactly as it
// already does for the UID.
func TestResourceVersionReachesTheCaptureOnBothPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{OutputDir: dir, LogLevel: "info", JsonExport: true}
	logger, err := NewLogger(cfg)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	c := &Controller{logger: logger, config: cfg}

	// Empty, so the ADDED has to come from the work item's snapshot - the
	// same state that produced the lost-ADDED bug.
	idx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	c.listers.Store("v1/configmaps@test-ns", cache.NewGenericLister(idx, schema.GroupResource{Resource: "configmaps"}))

	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("v1")
	obj.SetKind("ConfigMap")
	obj.SetName("cm-1")
	obj.SetNamespace("test-ns")
	obj.SetUID("uid-cm-1")
	obj.SetResourceVersion("182734")

	if err := c.reconcile(&WorkItem{
		Key: "test-ns/cm-1", informerNamespace: "test-ns", GVRString: "v1/configmaps", EventType: "ADDED",
		Configs: []NormalizedConfig{{GVR: "v1/configmaps"}}, Object: obj,
	}); err != nil {
		t.Fatalf("reconcile ADDED: %v", err)
	}
	if err := c.reconcile(&WorkItem{
		Key: "test-ns/cm-1", informerNamespace: "test-ns", GVRString: "v1/configmaps", EventType: "DELETED",
		Configs:    []NormalizedConfig{{GVR: "v1/configmaps"}},
		DeletedUID: "uid-cm-1", DeletedResourceVersion: "182999",
	}); err != nil {
		t.Fatalf("reconcile DELETED: %v", err)
	}
	logger.Shutdown()

	want := map[string]string{"ADDED": "182734", "DELETED": "182999"}
	seen := map[string]string{}
	for _, e := range captured(t, dir) {
		if e.Name == "cm-1" {
			seen[e.EventType] = e.ResourceVersion
		}
	}
	for action, rv := range want {
		got, ok := seen[action]
		if !ok {
			t.Fatalf("no %s event in the capture", action)
		}
		if got != rv {
			t.Errorf("%s resourceVersion = %q, want %q - without it this event cannot be matched against another collector's copy", action, got, rv)
		}
	}
}

// A synthetic event has no resourceVersion, and must not invent one.
//
// WATCH_FAILED is faro's own observation that a watch broke; no API change
// corresponds to it. Leaving it empty is what keeps two broken watches two
// events rather than one - NULLs are distinct in the unique index below, so
// the de-duplication that collapses a duplicated CHANGE never collapses these.
func TestWatchFailedCarriesNoResourceVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	l := &Logger{jsonFile: f}
	l.startCaptureWriter(f, true)
	c := &Controller{logger: l}
	c.emitWatchFailed("v1/pods", "kube-system", errors.New("connection reset by peer"))
	l.Shutdown()

	b, _ := os.ReadFile(path)
	var ev JSONEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), &ev); err != nil {
		t.Fatalf("capture line is not a JSONEvent: %v", err)
	}
	if ev.ResourceVersion != "" {
		t.Errorf("WATCH_FAILED carries resourceVersion %q; it is synthetic and must carry none", ev.ResourceVersion)
	}
	if !strings.Contains(string(b), "WATCH_FAILED") {
		t.Fatal("the event did not reach the capture at all")
	}
}

func rvEvent(name, uid, rv, action string, at time.Time) Event {
	je := JSONEvent{
		EventTime: at.UTC().Format(time.RFC3339Nano),
		EventType: action, GVR: "v1/configmaps", Namespace: "default",
		Name: name, UID: uid, ResourceVersion: rv,
	}
	b, _ := json.Marshal(je)
	return eventFromJSON(je, b)
}

// THE PROPERTY THE WHOLE CHANGE EXISTS FOR.
//
// Two collectors overlapping during a rollout both see the same change and
// both write it. They stamp it with different eventTimes, because eventTime is
// when the observer saw it - so time cannot tell a duplicate from a repeat.
// resourceVersion can, and the unique index drops the second copy.
//
// That is what makes a hand-over safe without coordinating a cut point: the
// incoming collector may start before the outgoing one stops, which is the
// only ordering that leaves no gap, and the overlap costs nothing.
func TestSQLiteDropsADuplicatedChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.db")
	s, err := newSQLiteSink(path, "collector-a")
	if err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	// The same change, seen by two collectors 400ms apart.
	s.Append(rvEvent("cm-1", "uid-1", "182734", "ADDED", at))
	s.Append(rvEvent("cm-1", "uid-1", "182734", "ADDED", at.Add(400*time.Millisecond)))
	// A LATER change to the same object: different resourceVersion, kept.
	s.Append(rvEvent("cm-1", "uid-1", "182999", "UPDATED", at.Add(time.Second)))
	// Same object, same resourceVersion, different action - a delete carries
	// the version it was deleted at, and must not be mistaken for the update.
	s.Append(rvEvent("cm-1", "uid-1", "182999", "DELETED", at.Add(2*time.Second)))
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	var n int
	db.QueryRow("select count(*) from events").Scan(&n)
	if n != 3 {
		t.Fatalf("capture holds %d rows, want 3 - the duplicate ADDED must be dropped and the other three kept", n)
	}
	var kept int
	db.QueryRow("select count(*) from events where action='ADDED' and resource_version='182734'").Scan(&kept)
	if kept != 1 {
		t.Errorf("the duplicated change is stored %d times, want 1", kept)
	}

	// The drop is counted, because it is the only remaining trace of an
	// informer re-list: a replay and an overlap copy are the same row, so the
	// index cannot keep one without the other. See the note in Flush.
	var n2 string
	if err := db.QueryRow("select v from meta where k='duplicates_dropped'").Scan(&n2); err != nil {
		t.Fatalf("no duplicates_dropped in meta: %v - the drop left no trace at all", err)
	}
	if n2 != "1" {
		t.Errorf("duplicates_dropped = %q, want 1", n2)
	}
}

// ...and must NOT collapse events that only look alike.
//
// Every WATCH_FAILED has a NULL resourceVersion, no uid and no name. If NULLs
// compared equal in the index, a collector whose watches kept breaking would
// record the first failure and silently discard the rest - turning the signal
// that something is wrong into the appearance that it happened once.
func TestSQLiteKeepsEveryWatchFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.db")
	s, err := newSQLiteSink(path, "n")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		s.Append(rvEvent("", "", "", "WATCH_FAILED", at.Add(time.Duration(i)*time.Second)))
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	var n int
	db.QueryRow("select count(*) from events where action='WATCH_FAILED'").Scan(&n)
	if n != 5 {
		t.Fatalf("5 broken watches recorded as %d rows; a repeated failure must stay a repeated failure", n)
	}
}

// Upgrading faro must not refuse the capture it was already writing.
//
// CREATE TABLE IF NOT EXISTS does nothing to a table that already exists, so
// an events table written by an older faro - or created by ingest-capture.py,
// which builds the same 15 columns - has no resource_version. The unique index
// names that column, and without the migration the sink fails at open with
// "no such column", which strands every existing capture.
func TestSQLiteSinkOpensACaptureFromBeforeThisColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// The 15-column table exactly as it was before this change.
	if _, err := db.Exec(`CREATE TABLE events (
	  ts REAL NOT NULL, ts_iso TEXT NOT NULL, source TEXT NOT NULL, node TEXT,
	  action TEXT, gvr TEXT, namespace TEXT, name TEXT, actor TEXT,
	  code INTEGER, detail TEXT, uid TEXT, labels TEXT, annotations TEXT, json TEXT);
	  INSERT INTO events (ts, ts_iso, source, action, gvr, uid)
	    VALUES (1,'old','faro','ADDED','v1/configmaps','uid-old'),
	           (2,'old','faro','ADDED','v1/configmaps','uid-old');`); err != nil {
		t.Fatalf("build the old capture: %v", err)
	}
	db.Close()

	// Two identical-looking legacy rows are present on purpose: they must not
	// make the new unique index impossible to build. Their backfilled
	// resource_version is NULL, and NULLs are distinct, so they cannot collide.
	s, err := newSQLiteSink(path, "n")
	if err != nil {
		t.Fatalf("faro cannot reopen a capture it wrote before the upgrade: %v", err)
	}
	s.Append(rvEvent("cm-new", "uid-new", "9001", "ADDED", time.Now()))
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, _ = sql.Open("sqlite", path)
	defer db.Close()
	var n int
	db.QueryRow("select count(*) from events").Scan(&n)
	if n != 3 {
		t.Fatalf("capture holds %d rows, want 3 - the two legacy rows plus the new one", n)
	}
	var rv sql.NullString
	db.QueryRow("select resource_version from events where uid='uid-new'").Scan(&rv)
	if !rv.Valid || rv.String != "9001" {
		t.Errorf("new row's resource_version = %v, want 9001", rv)
	}
	if err := db.QueryRow("select resource_version from events where uid='uid-old' limit 1").Scan(&rv); err != nil {
		t.Fatalf("legacy row: %v", err)
	}
	if rv.Valid {
		t.Errorf("legacy row backfilled with %q, want NULL - faro cannot know what version those changes had", rv.String)
	}
}

// Two collectors overlapping must not be pointed at one SQLite file.
//
// The overlap is the point - it is what leaves no gap - but SQLite's WAL needs
// shared memory between writers and the two pods are on different nodes
// sharing an RWX volume. They get a file each, and the capture is reassembled
// with ingest-capture.py's merge, which the unique index makes exact.
func TestSQLitePathIsPerCollectorUnderTheDeployment(t *testing.T) {
	cfg := &Config{OutputDir: t.TempDir(), SqlitePath: SqliteAuto}

	t.Setenv("FARO_POD_NAME", "")
	if got := sqliteTarget(cfg); !strings.HasSuffix(got, "/capture.db") {
		t.Errorf("standalone path = %q, want .../capture.db - a lone collector must reopen the capture it was already writing", got)
	}

	t.Setenv("FARO_POD_NAME", "faro-7d9c4b8f6-x2kqp")
	a := sqliteTarget(cfg)
	if !strings.HasSuffix(a, "/capture-faro-7d9c4b8f6-x2kqp.db") {
		t.Fatalf("pod path = %q, want it named after the pod", a)
	}
	t.Setenv("FARO_POD_NAME", "faro-6b5a39e21-m4tzw")
	if b := sqliteTarget(cfg); b == a {
		t.Errorf("both collectors resolve to %q; the surging pod would open the file the outgoing one is writing", b)
	}

	// An explicit path still wins, so the static pod and CI keep their own.
	cfg.SqlitePath = "/var/faro/bootstrap.db"
	if got := sqliteTarget(cfg); got != "/var/faro/bootstrap.db" {
		t.Errorf("explicit path = %q, want it honoured verbatim", got)
	}
}

// The drop count must survive the collector restarting.
//
// It is a running total in the database, not in the process. A restarted
// collector reopens the same capture with its own counter at zero, and if that
// were written straight out the total would go backwards across precisely the
// restarts it exists to describe - and a re-list, which is what a restart
// causes, is the largest source of drops there is.
func TestDuplicateCountAccumulatesAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.db")
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

	for round := 0; round < 3; round++ {
		s, err := newSQLiteSink(path, "n")
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		// The same change every round: the first round records it, and each
		// later round is a re-list replaying what is already there.
		s.Append(rvEvent("cm-1", "uid-1", "700", "ADDED", at))
		s.Append(rvEvent("cm-1", "uid-1", "700", "ADDED", at.Add(time.Second)))
		if err := s.Close(); err != nil {
			t.Fatalf("round %d close: %v", round, err)
		}
	}

	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	var rows int
	db.QueryRow("select count(*) from events").Scan(&rows)
	if rows != 1 {
		t.Errorf("capture holds %d rows, want 1 - it is the same change every time", rows)
	}
	var n string
	if err := db.QueryRow("select v from meta where k='duplicates_dropped'").Scan(&n); err != nil {
		t.Fatalf("no count: %v", err)
	}
	// One per round from the in-batch duplicate, plus one each in rounds two
	// and three for the change already on disk: 1 + 2 + 2.
	if n != "5" {
		t.Errorf("duplicates_dropped = %q, want 5 - the total reset when the collector restarted", n)
	}
}
