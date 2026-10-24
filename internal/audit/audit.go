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
	"time"
)

// Results an event can have.
const (
	OK     = "ok"
	Denied = "denied"
	Failed = "failed"
)

// Event is one audit record. Empty fields are left out of the output.
type Event struct {
	Time       time.Time `json:"time"`
	Event      string    `json:"event"`
	Result     string    `json:"result"`
	Error      string    `json:"error,omitempty"`
	Session    string    `json:"session,omitempty"`
	Account    string    `json:"account,omitempty"`
	IP         string    `json:"ip,omitempty"`
	Method     string    `json:"method,omitempty"`
	KeyFP      string    `json:"key_fp,omitempty"`
	User       string    `json:"user,omitempty"`
	Path       string    `json:"path,omitempty"`
	Storage    string    `json:"storage,omitempty"`
	Key        string    `json:"key,omitempty"`
	ToPath     string    `json:"to_path,omitempty"`
	ToKey      string    `json:"to_key,omitempty"`
	SHA256     string    `json:"sha256,omitempty"`
	Config     string    `json:"config,omitempty"`
	Job        string    `json:"job,omitempty"`
	TraceID    string    `json:"trace_id,omitempty"`
	Size       int64     `json:"size,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
}

var logger atomic.Pointer[slog.Logger]

func init() {
	SetOutput(os.Stdout)
}

// SetOutput sends audit lines to w. Tests use it to capture events.
func SetOutput(w io.Writer) {
	logger.Store(slog.New(slog.NewJSONHandler(w, nil)))
}

// Emit writes one event to the log and keeps it in the recent-events list.
func Emit(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	recent.add(e)

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
	add("trace_id", e.TraceID)
	if e.Size > 0 {
		attrs = append(attrs, slog.Int64("size", e.Size))
	}
	if e.DurationMS > 0 {
		attrs = append(attrs, slog.Int64("duration_ms", e.DurationMS))
	}

	logger.Load().LogAttrs(context.Background(), slog.LevelInfo, "audit", attrs...)
}
