package faro

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

// The schema is SHARED with the analysis side, not private to faro.
//
// kinc_fleet's tools/ingest-capture.py builds exactly this `events` table and
// adds apiserver audit rows and kill marks to it, keyed on epoch seconds, so
// one timeline holds every vantage point. faro writing the same table is what
// lets that tool stop parsing JSON and simply open what faro already wrote.
//
// The first twelve columns are that tool's, in its order. The last three are
// faro's own: labels and annotations, which the JSON capture carried and a
// 12-column table had nowhere to put, and the canonical JSON itself - so the
// database is a superset of the file rather than a lossy copy of it.
//
// No indexes beyond these and no retention policy: what happens to the data
// after it is accepted belongs to whoever runs the database.
const sqliteSchema = `
CREATE TABLE IF NOT EXISTS events (
  ts        REAL NOT NULL,
  ts_iso    TEXT NOT NULL,
  source    TEXT NOT NULL,
  node      TEXT,
  action    TEXT,
  gvr       TEXT,
  namespace TEXT,
  name      TEXT,
  actor     TEXT,
  code      INTEGER,
  detail    TEXT,
  uid       TEXT,
  labels      TEXT,
  annotations TEXT,
  json        TEXT,
  resource_version TEXT
);
CREATE INDEX IF NOT EXISTS ix_ts     ON events(ts);
CREATE INDEX IF NOT EXISTS ix_source ON events(source, ts);
CREATE INDEX IF NOT EXISTS ix_gvr    ON events(gvr, ts);
CREATE INDEX IF NOT EXISTS ix_uid    ON events(uid, ts);
-- WHAT MAKES AN OVERLAP HARMLESS.
--
-- Two collectors watching the same stream stamp different eventTimes for one
-- change - eventTime is the observer's, not the change's - but they see the
-- same resourceVersion. So this identifies the CHANGE, and a second copy of it
-- is dropped rather than recorded twice.
--
-- That is what lets a rollout overlap deliberately instead of coordinating a
-- cut-over: there is no cut point to agree on and no clock to synchronise,
-- because writing the same change twice costs nothing.
--
-- A re-list replays the same resourceVersion too, so this makes an informer
-- replay exactly detectable rather than inferred from "an earlier ADDED with
-- this UID".
--
-- WATCH_FAILED is synthetic and has no resourceVersion. NULLs are distinct in
-- a unique index in both SQLite and Postgres, so those rows never collide with
-- each other - which is what we want: two broken watches are two events.
CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT);
`

// Applied after sqliteSchema, because the index names a column that a database
// created by an OLDER faro - or by ingest-capture.py - does not have, and
// CREATE TABLE IF NOT EXISTS will not add it to a table that already exists.
// Without this, upgrading faro against an existing capture fails at open with
// "no such column: resource_version", which is precisely the reopen-in-place
// case this sink was built for.
//
// The backfilled column is NULL on every pre-existing row, and NULLs are
// distinct in a unique index, so the index can never collide with legacy data
// however many duplicates it holds.
const sqliteMigrate = `ALTER TABLE events ADD COLUMN resource_version TEXT`

const sqliteChangeIndex = `CREATE UNIQUE INDEX IF NOT EXISTS ux_change ON events(gvr, uid, resource_version, action)`

func init() {
	RegisterSink("sqlite", func(config *Config, l *Logger) (Sink, error) {
		path := sqliteTarget(config)
		if path == "" {
			return nil, nil
		}
		s, err := newSQLiteSink(path, os.Getenv("FARO_NODE"))
		if err != nil {
			return nil, err
		}
		fmt.Printf("FARO_SQLITE_FILE: %s\n", path)
		return s, nil
	})
}

// SqliteAuto puts the SQLite capture in the default place under output_dir.
// It is what the faro binary uses unless configured otherwise; a Config built
// in code has no database unless it sets this or a path.
const SqliteAuto = "auto"

// sqliteSink writes the capture into a SQLite database.
//
// One database, reopened in place across restarts rather than a new file per
// start. That is safe because the Deployment runs Recreate, so one collector
// writes at a time - and because it was measured: a long-lived connection with
// a 4.1MB uncheckpointed WAL, on a SeaweedFS RWX volume, with the VM hosting
// the writer destroyed mid-transaction. The database opened clean afterwards,
// every acknowledged commit present, no torn transaction and no gap.
type sqliteSink struct {
	db      *sql.DB
	node    string
	pending []Event
	// How many writes the unique index refused. See the note in Flush.
	dropped int64
}

func newSQLiteSink(path, node string) (*sqliteSink, error) {
	// WAL for the throughput: measured flat at 94-97ms per commit across 2000
	// commits on the lab's network volume, where the append-and-fsync JSON file
	// degraded 20ms -> 167ms over the same span because every fsync appends a
	// chunk. WAL checkpointing recycles instead of accumulating.
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// One writer. More connections would mean more writers contending for the
	// same file, which is the thing Recreate exists to prevent.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	// An error here is the column already existing, which is the normal case on
	// every open but the first against an old database. SQLite has no
	// ADD COLUMN IF NOT EXISTS, so the duplicate is detected by trying.
	db.Exec(sqliteMigrate)
	if _, err := db.Exec(sqliteChangeIndex); err != nil {
		db.Close()
		return nil, fmt.Errorf("change index: %w", err)
	}
	if node == "" {
		node, _ = os.Hostname()
	}
	return &sqliteSink{db: db, node: node}, nil
}

func (s *sqliteSink) Append(e Event) error {
	s.pending = append(s.pending, e)
	return nil
}

// Flush writes everything buffered in ONE transaction.
//
// Batching is not an optimisation here, it is the difference between usable
// and not: measured on the lab's Postgres, ~10 rows/s committing one at a time
// against 272 rows/s at 50 rows per transaction. SQLite behaves the same way,
// for the same reason - the cost is the commit, not the row.
func (s *sqliteSink) Flush() error {
	if len(s.pending) == 0 {
		return nil
	}
	batch := s.pending
	s.pending = nil

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT INTO events
	  (ts, ts_iso, source, node, action, gvr, namespace, name, actor, code, detail, uid, labels, annotations, json, resource_version)
	  VALUES (?,?,'faro',?,?,?,?,?,NULL,NULL,?,?,?,?,?,?)
	  ON CONFLICT DO NOTHING`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer st.Close()

	// WHY THE DROPS ARE COUNTED.
	//
	// A re-list replay - the ADDED an informer re-emits for an object that
	// already existed - is byte-for-byte the same change as the copy a second
	// collector writes during an overlap. The index cannot keep one and drop
	// the other, because nothing distinguishes them but which process wrote it,
	// and ignoring that is the entire point.
	//
	// So the replays stop appearing as rows, and kinc_fleet's faro_events view
	// can no longer mark them. That signal is real - "when did the collector
	// re-list" was answered by those rows, and one measured run found 1079 of
	// them, 85% of a capture's ADDEDs - so it is kept as a count rather than
	// quietly lost. Non-zero with no rollout in progress means re-listing.
	var dropped int64
	for _, e := range batch {
		t := e.EventTime
		if t.IsZero() {
			t = time.Now().UTC()
		}
		// Argument order follows the column list exactly: source is the
		// literal 'faro', actor and code are NULL and belong to audit rows.
		res, err := st.Exec(
			float64(t.UnixNano())/1e9, t.UTC().Format(time.RFC3339Nano),
			s.node, e.EventType, e.GVR, nullable(e.Namespace), e.Name,
			jsonOrNull(e.Fields), nullable(e.UID),
			jsonOrNull(e.Labels), jsonOrNull(e.Annotations), string(e.JSON),
			nullable(e.ResourceVersion),
		)
		if err != nil {
			tx.Rollback()
			return err
		}
		// ON CONFLICT DO NOTHING reports zero rows changed for a write the
		// index refused.
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			dropped++
		}
	}
	// In the same transaction as the rows it describes, so a collector killed
	// between the two cannot leave a count that disagrees with the capture.
	if dropped > 0 {
		s.dropped += dropped
		// Accumulated in SQL rather than from the counter in this struct,
		// which starts at zero every time the process does. A collector that
		// restarts and reopens the capture would otherwise overwrite the
		// running total with its own much smaller one, and the number would
		// silently go backwards across exactly the restarts it is there to
		// describe.
		if _, err := tx.Exec(`INSERT INTO meta (k, v) VALUES ('duplicates_dropped', ?)
		  ON CONFLICT(k) DO UPDATE SET v = CAST(CAST(meta.v AS INTEGER) + ? AS TEXT)`,
			fmt.Sprint(dropped), dropped); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *sqliteSink) Close() error {
	err := s.Flush()
	if cerr := s.db.Close(); err == nil {
		err = cerr
	}
	return err
}

func nullable(v string) interface{} {
	if v == "" {
		return nil
	}
	return v
}

// jsonOrNull keeps an absent map out of the column entirely rather than
// storing "null" or "{}", so json_extract on it behaves the way a reader
// expects and a capture taken without fields looks the same as it always did.
func jsonOrNull(v interface{}) interface{} {
	switch m := v.(type) {
	case map[string]string:
		if len(m) == 0 {
			return nil
		}
	case map[string]interface{}:
		if len(m) == 0 {
			return nil
		}
	case nil:
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return string(b)
}

// sqliteTarget resolves where the SQLite capture goes. "off" disables it;
// empty means the default beside the other capture files.
// ONE FILE PER COLLECTOR, NOT ONE PER VOLUME.
//
// Two collectors deliberately overlap during a rollout - that is what closes
// the gap, and the unique index on (gvr, uid, resource_version, action) is what
// makes the duplicated changes free. What is NOT free is both of them opening
// the same SQLite file: they mount one RWX volume, and SQLite's WAL mode needs
// shared memory between writers, which a network filesystem does not provide
// across clients. Local locking would not save it either - the two writers are
// on different nodes.
//
// So the overlap is given two files and the capture is reassembled afterwards:
//
//	kinc_fleet/tools/ingest-capture.py <main.db> merge <other.db>
//
// which copies rows whole and lets the unique index collapse the changes both
// collectors saw. That merge is exact, not heuristic - the overlap is
// recognised by resourceVersion rather than guessed at from timestamps.
//
// FARO_POD_NAME comes from the downward API and is only set under the
// Deployment. Without it - the static bootstrap pod, the CI run, a local
// binary - nothing is sharing the directory and the name stays capture.db, so
// a restart reopens the capture it was already writing.
func sqliteTarget(config *Config) string {
	switch config.SqlitePath {
	case "off", "":
		return ""
	case SqliteAuto:
		d := config.GetLogDir()
		if d == "" {
			return ""
		}
		if pod := os.Getenv("FARO_POD_NAME"); pod != "" {
			return d + "/capture-" + pod + ".db"
		}
		return d + "/capture.db"
	default:
		return config.SqlitePath
	}
}
