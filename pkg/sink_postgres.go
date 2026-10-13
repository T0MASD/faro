package faro

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"k8s.io/klog/v2"
)

// The same record as the SQLite capture, in Postgres types.
//
// The column list and its order match sink_sqlite.go deliberately: one record
// shape across destinations means a query written against one reads the other,
// and the analysis side does not need to know where a capture came from. jsonb
// rather than text for the three JSON columns, because that is the whole point
// of being in Postgres - they are queryable and indexable here.
const postgresSchema = `
CREATE TABLE IF NOT EXISTS events (
  ts          double precision NOT NULL,
  ts_iso      timestamptz      NOT NULL,
  source      text             NOT NULL,
  node        text,
  action      text,
  gvr         text,
  namespace   text,
  name        text,
  actor       text,
  code        integer,
  detail      jsonb,
  uid         text,
  labels      jsonb,
  annotations jsonb,
  json        jsonb,
  resource_version text
);
ALTER TABLE events ADD COLUMN IF NOT EXISTS resource_version text;
CREATE INDEX IF NOT EXISTS ix_events_ts     ON events(ts);
CREATE INDEX IF NOT EXISTS ix_events_source ON events(source, ts);
CREATE INDEX IF NOT EXISTS ix_events_gvr    ON events(gvr, ts);
CREATE INDEX IF NOT EXISTS ix_events_uid    ON events(uid, ts);
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
CREATE UNIQUE INDEX IF NOT EXISTS ux_events_change ON events(gvr, uid, resource_version, action);
`

const (
	// How many events may wait for the database before the sink starts
	// dropping. See the note on Append.
	postgresQueue = 8192
	// Rows per statement. Measured on the lab's Postgres: ~10 rows/s
	// committing one at a time against 272 rows/s at 50 per transaction.
	postgresBatch = 200
	// How often a partial batch goes anyway, so a quiet cluster still lands.
	postgresInterval = time.Second
)

func init() {
	RegisterSink("postgres", func(config *Config, l *Logger) (Sink, error) {
		dsn := postgresDSN(config)
		if dsn == "" {
			return nil, nil
		}
		s, err := newPostgresSink(dsn, os.Getenv("FARO_NODE"))
		if err != nil {
			return nil, err
		}
		fmt.Println("FARO_POSTGRES: attached")
		return s, nil
	})
}

// postgresSink sends the capture to Postgres.
//
// Unlike the file and SQLite sinks it is ASYNCHRONOUS INTERNALLY, and that is
// the important difference. Those two write to local storage: when they are
// slow, waiting for them is correct backpressure. A database reached over a
// network is not local storage - if it is down or slow, waiting for it would
// stop the capture writer, and a database outage would become a capture
// outage. The last several releases were about making the capture survive
// things; this must not undo that.
//
// So Append hands over and returns, a goroutine of its own talks to the
// database, and a queue that fills DROPS. Losing rows from a secondary
// destination is a smaller harm than stalling the primary one.
type postgresSink struct {
	db   *sql.DB
	node string

	ch   chan Event
	done chan struct{}

	dropped   atomic.Uint64
	reported  atomic.Uint64
	schemaOK  atomic.Bool
	lastError atomic.Pointer[string]
}

func newPostgresSink(dsn, node string) (*postgresSink, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	// Deliberately NOT pinging here. faro does not check that a destination is
	// reachable before starting: a database that is down at boot and up a
	// minute later should capture from the minute it is up, not refuse to
	// start. database/sql opens lazily and reconnects on its own.
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if node == "" {
		node, _ = os.Hostname()
	}
	s := &postgresSink{db: db, node: node, ch: make(chan Event, postgresQueue), done: make(chan struct{})}
	go s.run()
	return s, nil
}

// Append never blocks.
//
// A full queue means the database cannot keep up with the cluster, and the
// answer is to drop rather than to wait - see the type comment. The drop is
// counted and reported, never silent.
func (p *postgresSink) Append(e Event) error {
	select {
	case p.ch <- e:
		return nil
	default:
		n := p.dropped.Add(1)
		// Report the first, then on a widening interval: a destination that
		// cannot keep up drops continuously, and a line per row would bury
		// everything else in the log.
		if n == 1 || n%1000 == 0 {
			klog.Errorf("[postgres] capture queue is full, %d event(s) dropped so far; the database is not keeping up", n)
		}
		return nil
	}
}

// Flush nudges the worker and returns.
//
// It does NOT wait for the database. FlushCapture means "everything enqueued
// before me is durable", and this sink cannot honour that without blocking on
// a network - which is the one thing it exists not to do. A destination that
// is allowed to drop cannot also promise durability on demand; for that, make
// it the only sink and feel its backpressure directly.
func (p *postgresSink) Flush() error { return nil }

func (p *postgresSink) Close() error {
	close(p.ch)
	<-p.done
	if n := p.dropped.Load(); n > 0 {
		klog.Errorf("[postgres] %d event(s) were dropped over this run", n)
	}
	return p.db.Close()
}

func (p *postgresSink) run() {
	defer close(p.done)
	t := time.NewTicker(postgresInterval)
	defer t.Stop()
	batch := make([]Event, 0, postgresBatch)

	send := func() {
		if len(batch) == 0 {
			return
		}
		if err := p.insert(batch); err != nil {
			// Say so once per distinct error rather than per batch: a database
			// that is down produces the same message forever.
			msg := err.Error()
			if prev := p.lastError.Load(); prev == nil || *prev != msg {
				p.lastError.Store(&msg)
				klog.Errorf("[postgres] %d event(s) not written: %v", len(batch), err)
			}
		} else if p.lastError.Load() != nil {
			p.lastError.Store(nil)
			klog.Infof("[postgres] writing again")
		}
		batch = batch[:0]
	}

	for {
		select {
		case e, ok := <-p.ch:
			if !ok {
				send()
				return
			}
			batch = append(batch, e)
			if len(batch) >= postgresBatch {
				send()
			}
		case <-t.C:
			send()
		}
	}
}

// ensureSchema runs the one idempotent statement faro owns.
//
// Retried until it succeeds rather than done once at open, because the
// database may not be there at boot. Everything after it is a plain insert:
// retention, partitioning, extra indexes and vacuuming belong to whoever runs
// the database.
func (p *postgresSink) ensureSchema() error {
	if p.schemaOK.Load() {
		return nil
	}
	if _, err := p.db.Exec(postgresSchema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	p.schemaOK.Store(true)
	return nil
}

func (p *postgresSink) insert(batch []Event) error {
	if err := p.ensureSchema(); err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString(`INSERT INTO events (ts, ts_iso, source, node, action, gvr, namespace, name, actor, code, detail, uid, labels, annotations, json, resource_version) VALUES `)
	args := make([]interface{}, 0, len(batch)*13)
	for i, e := range batch {
		if i > 0 {
			sb.WriteByte(',')
		}
		n := i * 13
		fmt.Fprintf(&sb, "($%d,$%d,'faro',$%d,$%d,$%d,$%d,$%d,NULL,NULL,$%d,$%d,$%d,$%d,$%d,$%d)",
			n+1, n+2, n+3, n+4, n+5, n+6, n+7, n+8, n+9, n+10, n+11, n+12, n+13)
		t := e.EventTime
		if t.IsZero() {
			t = time.Now().UTC()
		}
		args = append(args,
			float64(t.UnixNano())/1e9, t.UTC(),
			p.node, e.EventType, e.GVR, nullable(e.Namespace), e.Name,
			jsonOrNull(e.Fields), nullable(e.UID),
			jsonOrNull(e.Labels), jsonOrNull(e.Annotations), string(e.JSON),
			nullable(e.ResourceVersion))
	}
	sb.WriteString(" ON CONFLICT DO NOTHING")
	_, err := p.db.Exec(sb.String(), args...)
	return err
}

// openForTest gives the tests a plain connection to verify what landed.
func openForTest(dsn string) (*sql.DB, error) { return sql.Open("pgx", dsn) }

// postgresDSN reads the connection string from where it is kept, never from
// the config itself. See Config.PostgresDSNFile.
//
// The file's whole contents are the DSN, trimmed: that is what a Secret volume
// and a file on a node both look like. An unreadable path is reported and
// skipped rather than fatal - Postgres is the optional destination, and losing
// it must not cost the capture.
func postgresDSN(config *Config) string {
	if v := strings.TrimSpace(os.Getenv("FARO_POSTGRES_DSN")); v != "" {
		return v
	}
	if config.PostgresDSNFile == "" {
		return ""
	}
	b, err := os.ReadFile(config.PostgresDSNFile)
	if err != nil {
		klog.Errorf("[logger] postgres_dsn_file %s could not be read, continuing without Postgres: %v",
			config.PostgresDSNFile, err)
		return ""
	}
	return strings.TrimSpace(string(b))
}
