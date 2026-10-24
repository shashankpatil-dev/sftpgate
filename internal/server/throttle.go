package server

import (
	"net/netip"
	"sync"
	"time"

	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/metrics"
)

// Throttle blocks IPs that fail to log in too many times.
//
// It counts failed logins per IP inside a time window. Once an IP reaches
// the limit, all its new connections are refused until the block ends.
// A successful login does not reset the count, so an attacker can't mix in
// one good login to keep guessing.
type Throttle struct {
	// Now returns the current time. Tests replace it to move time forward.
	Now func() time.Time

	mu      sync.Mutex
	entries map[netip.Addr]*throttleEntry
}

type throttleEntry struct {
	fails        int
	first        time.Time
	blockedUntil time.Time
}

// NewThrottle returns an empty throttle.
func NewThrottle() *Throttle {
	return &Throttle{Now: time.Now, entries: map[netip.Addr]*throttleEntry{}}
}

// Blocked reports whether ip is blocked right now.
func (t *Throttle) Blocked(ip netip.Addr) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	e := t.entries[ip]
	return e != nil && t.Now().Before(e.blockedUntil)
}

// Fail counts one failed login from ip.
func (t *Throttle) Fail(ip netip.Addr, rules config.Throttle) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.Now()
	e := t.entries[ip]
	// Start counting again if there is no entry or the old window is over.
	if e == nil || now.Sub(e.first) > rules.Window {
		e = &throttleEntry{first: now}
		t.entries[ip] = e
	}

	e.fails++
	if e.fails >= rules.MaxFailures {
		e.blockedUntil = now.Add(rules.Block)
	}
	t.updateMetric(now)
}

// Sweep forgets IPs whose window and block are both over, so the map
// doesn't grow forever. The server calls it once a minute.
func (t *Throttle) Sweep(rules config.Throttle) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.Now()
	for ip, e := range t.entries {
		if now.Sub(e.first) > rules.Window && !now.Before(e.blockedUntil) {
			delete(t.entries, ip)
		}
	}
	t.updateMetric(now)
}

func (t *Throttle) updateMetric(now time.Time) {
	blocked := 0
	for _, e := range t.entries {
		if now.Before(e.blockedUntil) {
			blocked++
		}
	}
	metrics.ThrottledIPs.Set(float64(blocked))
}
