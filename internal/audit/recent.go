package audit

import "sync"

// recentSize is how many events are kept in memory for the web UI.
// Older history lives in the log collector, not here.
const recentSize = 2000

var recent = &ring{events: make([]Event, recentSize)}

// ring keeps the last recentSize events, overwriting the oldest.
type ring struct {
	mu     sync.Mutex
	events []Event
	next   int
	full   bool
}

func (r *ring) add(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events[r.next] = e
	r.next = (r.next + 1) % len(r.events)
	if r.next == 0 {
		r.full = true
	}
}

// Filter picks which recent events to return. Empty fields match anything.
type Filter struct {
	Event   string
	Account string
	Result  string
	Limit   int
}

// Recent returns the newest events that match f, newest first.
//
// shortcut: kept in memory per process, so it is empty after a restart and
// each replica has its own; durable history is the log collector's job.
func Recent(f Filter) []Event {
	recent.mu.Lock()
	defer recent.mu.Unlock()

	count := recent.next
	if recent.full {
		count = len(recent.events)
	}

	var out []Event
	for i := 1; i <= count && (f.Limit <= 0 || len(out) < f.Limit); i++ {
		e := recent.events[(recent.next-i+len(recent.events))%len(recent.events)]
		if matches(e, f) {
			out = append(out, e)
		}
	}
	return out
}

func matches(e Event, f Filter) bool {
	return (f.Event == "" || e.Event == f.Event) &&
		(f.Account == "" || e.Account == f.Account) &&
		(f.Result == "" || e.Result == f.Result)
}
