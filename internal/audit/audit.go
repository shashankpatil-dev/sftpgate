// Package audit writes one JSON line to stdout for every security-relevant
// event: connections, logins and file operations. The org's log collector
// picks these lines up and stores them.
package audit

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
)

// Results an event can have.
const (
	OK     = "ok"
	Denied = "denied"
	Failed = "failed"
)

// Event is one audit record. Empty fields are left out of the output.
type Event struct {
	Event      string
	Result     string
	Error      string
	Session    string
	Account    string
	IP         string
	Method     string
	KeyFP      string
	User       string
	Path       string
	Storage    string
	Key        string
	ToPath     string
	ToKey      string
	SHA256     string
	Config     string
	Job        string
	Size       int64
	DurationMS int64
}

var logger atomic.Pointer[slog.Logger]

func init() {
	SetOutput(os.Stdout)
}

// SetOutput sends audit lines to w. Tests use it to capture events.
func SetOutput(w io.Writer) {
	logger.Store(slog.New(slog.NewJSONHandler(w, nil)))
}

// Emit writes one event.
func Emit(e Event) {
	var attrs []slog.Attr
	add := func(k, v string) {
		if v != "" {
			attrs = append(attrs, slog.String(k, v))
		}
	}
	add("event", e.Event)
	add("result", e.Result)
	add("error", e.Error)
	add("session", e.Session)
	add("account", e.Account)
	add("ip", e.IP)
	add("method", e.Method)
	add("key_fp", e.KeyFP)
	add("user", e.User)
	add("path", e.Path)
	add("storage", e.Storage)
	add("key", e.Key)
	add("to_path", e.ToPath)
	add("to_key", e.ToKey)
	add("sha256", e.SHA256)
	add("config", e.Config)
	add("job", e.Job)
	if e.Size > 0 {
		attrs = append(attrs, slog.Int64("size", e.Size))
	}
	if e.DurationMS > 0 {
		attrs = append(attrs, slog.Int64("duration_ms", e.DurationMS))
	}

	logger.Load().LogAttrs(context.Background(), slog.LevelInfo, "audit", attrs...)
}
