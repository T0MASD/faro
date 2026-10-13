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
  json        TEXT
);
CREATE INDEX IF NOT EXISTS ix_ts     ON events(ts);
CREATE INDEX IF NOT EXISTS ix_source ON events(source, ts);
CREATE INDEX IF NOT EXISTS ix_gvr    ON events(gvr, ts);
CREATE INDEX IF NOT EXISTS ix_uid    ON events(uid, ts);
CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT);
`

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
	  (ts, ts_iso, source, node, action, gvr, namespace, name, actor, code, detail, uid, labels, annotations, json)
	  VALUES (?,?,'faro',?,?,?,?,?,NULL,NULL,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer st.Close()

	for _, e := range batch {
		t := e.EventTime
		if t.IsZero() {
			t = time.Now().UTC()
		}
		// Argument order follows the column list exactly: source is the
		// literal 'faro', actor and code are NULL and belong to audit rows.
		if _, err := st.Exec(
			float64(t.UnixNano())/1e9, t.UTC().Format(time.RFC3339Nano),
			s.node, e.EventType, e.GVR, nullable(e.Namespace), e.Name,
			jsonOrNull(e.Fields), nullable(e.UID),
			jsonOrNull(e.Labels), jsonOrNull(e.Annotations), string(e.JSON),
		); err != nil {
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
func sqliteTarget(config *Config) string {
	switch config.SqlitePath {
	case "off":
		return ""
	case "":
		if d := config.GetLogDir(); d != "" {
			return d + "/capture.db"
		}
		return ""
	default:
		return config.SqlitePath
	}
}
