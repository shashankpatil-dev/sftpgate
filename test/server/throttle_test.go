package server_test

import (
	"net/netip"
	"testing"
	"time"

	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/server"
)

func TestThrottle(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	th := server.NewThrottle()
	th.Now = func() time.Time { return now }

	rules := config.Throttle{MaxFailures: 3, Window: 10 * time.Minute, Block: 15 * time.Minute}
	bad := netip.MustParseAddr("203.0.113.9")
	other := netip.MustParseAddr("203.0.113.10")

	th.Fail(bad, rules)
	th.Fail(bad, rules)
	if th.Blocked(bad) {
		t.Fatal("blocked before reaching max_failures")
	}

	th.Fail(bad, rules)
	if !th.Blocked(bad) {
		t.Fatal("not blocked after max_failures")
	}
	if th.Blocked(other) {
		t.Fatal("another IP must not be blocked")
	}

	now = now.Add(16 * time.Minute)
	if th.Blocked(bad) {
		t.Fatal("still blocked after the block time")
	}

	// Failures spread out over more than the window never add up to a block.
	for i := 0; i < 5; i++ {
		th.Fail(other, rules)
		now = now.Add(11 * time.Minute)
		if th.Blocked(other) {
			t.Fatal("slow failures outside the window must not block")
		}
	}
}
