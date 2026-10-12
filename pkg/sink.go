package faro

import "time"

// An Event is one thing that happened, as faro observed it.
//
// It carries both the decoded values and the canonical JSON, because the
// destinations want different halves: a file writes the bytes and must never
// re-marshal them, and a SQL destination wants the values as columns and must
// never re-parse them. Producing both once, where the event is built, is what
// lets a capture go to several places without either cost.
type Event struct {
	// When faro SAW this, as distinct from when the object was created.
	EventTime time.Time

	EventType string // ADDED | UPDATED | DELETED | WATCH_FAILED
	GVR       string
	Namespace string // empty for a cluster-scoped object
	Name      string
	UID       string

	// The object's resourceVersion at this change: the only field that
	// identifies the CHANGE rather than the observer, which is what makes a
	// duplicate recognisable. Empty for WATCH_FAILED, which describes no
	// object.
	ResourceVersion string

	Labels      map[string]string
	Annotations map[string]string
	Fields      map[string]interface{}

	// The canonical encoding, exactly as a file capture holds it. Never nil
	// for an event that reached a sink.
	JSON []byte
}

// A Sink is somewhere a capture goes.
//
// That is the whole contract, and it is deliberately thin: faro hands the
// event over and assumes the destination deals with it. faro does not retry on
// a sink's behalf, does not check that it is reachable, and does not manage
// what happens to the data once accepted - retention, indexes, partitioning
// and vacuuming all belong to whoever runs the destination.
//
// What faro does guarantee is isolation. A sink that errors, blocks or falls
// behind is its own problem: it must not stop another sink, and it must not
// stop the capture.
type Sink interface {
	// Append hands over one event. Returning an error is how a sink reports
	// that this event did not land; faro records that and carries on.
	Append(Event) error

	// Flush makes durable everything handed over so far.
	Flush() error

	// Close flushes and releases whatever the sink holds.
	Close() error
}
