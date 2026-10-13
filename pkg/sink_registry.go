package faro

import "sort"

// A SinkFactory builds one capture destination from the configuration, or
// returns (nil, nil) when that destination is not configured.
//
// It receives the Logger because a destination may need to hang lifecycle on
// it - the file sink keeps its handle and its rotation opener there - but a
// factory that needs nothing from it should ignore it.
type SinkFactory func(*Config, *Logger) (Sink, error)

type registeredSink struct {
	name  string
	build SinkFactory
}

var sinkRegistry []registeredSink

// RegisterSink adds a capture destination.
//
// Called from a backend's init(), so a destination is ADDED BY ADDING A FILE:
// nothing in NewLogger names the backends, and nothing has to be edited to
// introduce another. It is also what would make a build tag sufficient on its
// own - excluding the file excludes the backend, with no stub to keep in step
// and no branch left behind referring to it.
//
// Registration order does not matter; destinations are built in name order so
// a run reports them the same way twice.
func RegisterSink(name string, build SinkFactory) {
	sinkRegistry = append(sinkRegistry, registeredSink{name: name, build: build})
}

// buildSinks asks every registered destination whether it is configured.
//
// A destination that cannot be built is reported and SKIPPED, never fatal: the
// others still work, and no capture at all is the worse outcome. That is the
// same isolation the writer gives a sink at runtime, applied at startup.
func buildSinks(config *Config, l *Logger) []Sink {
	reg := make([]registeredSink, len(sinkRegistry))
	copy(reg, sinkRegistry)
	sort.Slice(reg, func(i, j int) bool { return reg[i].name < reg[j].name })

	var out []Sink
	for _, r := range reg {
		s, err := r.build(config, l)
		if err != nil {
			klogErrorf("[logger] capture destination %q could not be set up, continuing without it: %v", r.name, err)
			continue
		}
		if s != nil {
			out = append(out, s)
		}
	}
	return out
}
