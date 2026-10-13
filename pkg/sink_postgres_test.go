package faro

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The DSN is read from a FILE, never from the config.
//
// That config is baked into the kinc node image and published as a ConfigMap,
// so a literal DSN there would put a password in a registry. A path is also
// the one mechanism that covers both deployments, because a Secret volume and
// a file on a node look identical to the process reading them.
func TestPostgresDSNComesFromAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "postgres-dsn")
	// Trailing newline on purpose: that is what a Secret volume and an
	// `echo > file` both produce, and a DSN with a newline in it does not
	// connect.
	if err := os.WriteFile(path, []byte("postgres://u:p@h:5432/db?sslmode=disable\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := postgresDSN(&Config{PostgresDSNFile: path})
	if got != "postgres://u:p@h:5432/db?sslmode=disable" {
		t.Fatalf("DSN = %q; the file's contents are the DSN, trimmed", got)
	}

	// No file and no env means no Postgres. There is no default endpoint.
	if got := postgresDSN(&Config{}); got != "" {
		t.Fatalf("DSN = %q with nothing configured, want empty", got)
	}

	// An unreadable path must not be fatal: Postgres is the optional
	// destination and losing it must not cost the capture.
	if got := postgresDSN(&Config{PostgresDSNFile: filepath.Join(dir, "nope")}); got != "" {
		t.Fatalf("DSN = %q from a missing file, want empty", got)
	}
}

func TestPostgresEnvOverridesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dsn")
	os.WriteFile(path, []byte("from-file"), 0600)
	t.Setenv("FARO_POSTGRES_DSN", "from-env")
	if got := postgresDSN(&Config{PostgresDSNFile: path}); got != "from-env" {
		t.Fatalf("DSN = %q, want the environment to win", got)
	}
}

// A sink pointed at a database that is not there must not block, must not
// panic, and must not stop the capture. This is the whole reason it is
// asynchronous internally.
func TestPostgresSinkSurvivesAnAbsentDatabase(t *testing.T) {
	s, err := newPostgresSink("postgres://nobody:nothing@127.0.0.1:1/none?sslmode=disable&connect_timeout=1", "n")
	if err != nil {
		t.Fatalf("open should be lazy and not fail: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			if err := s.Append(sqliteEvent("x", time.Now())); err != nil {
				t.Errorf("Append returned %v; it must never fail the caller", err)
			}
		}
		if err := s.Flush(); err != nil {
			t.Errorf("Flush returned %v; it must not wait on the network", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Append/Flush blocked on an unreachable database - a database outage must not become a capture outage")
	}
	s.Close()
}

// A full queue drops rather than waits, and says so.
func TestPostgresSinkDropsWhenTheQueueIsFull(t *testing.T) {
	s := &postgresSink{ch: make(chan Event, 4), done: make(chan struct{})}
	close(s.done) // no worker: nothing drains, so the queue fills
	for i := 0; i < 50; i++ {
		if err := s.Append(sqliteEvent("x", time.Now())); err != nil {
			t.Fatalf("Append returned %v; a drop is reported, not returned as a failure", err)
		}
	}
	if n := s.dropped.Load(); n == 0 {
		t.Fatal("nothing was recorded as dropped; a silent drop is the failure this design exists to avoid")
	}
	if got := len(s.ch); got != 4 {
		t.Fatalf("queue holds %d, want it capped at 4", got)
	}
}

// Against a real Postgres when one is reachable. Skipped otherwise, so the
// suite does not depend on a database being up.
func TestPostgresSinkAgainstARealDatabase(t *testing.T) {
	dsn := os.Getenv("FARO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("FARO_TEST_POSTGRES_DSN not set")
	}
	s, err := newPostgresSink(dsn, "test-node")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	name := "cm-" + strings.ReplaceAll(t.Name(), "/", "-")
	at := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < 120; i++ {
		s.Append(sqliteEvent(name, at))
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
	if err := db.QueryRow("select count(*) from events where name=$1 and source='faro'", name).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 120 {
		t.Fatalf("database holds %d rows for this run, want 120", n)
	}
	var reason string
	if err := db.QueryRow("select detail->>'reason' from events where name=$1 limit 1", name).Scan(&reason); err != nil || reason != "Created" {
		t.Fatalf("detail->>'reason' = %q (err %v), want Created - jsonb is the point of being in Postgres", reason, err)
	}
	db.Exec("delete from events where name=$1", name)
}
