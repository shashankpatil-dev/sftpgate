package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/shashankpatil-dev/sftpgate/internal/actions"
	"github.com/shashankpatil-dev/sftpgate/internal/audit"
)

// Status is the summary shown at the top of the UI and by /api/v1/status.
type Status struct {
	Version        string    `json:"version"`
	Started        time.Time `json:"started"`
	UptimeSeconds  int64     `json:"uptime_seconds"`
	ConfigHash     string    `json:"config_hash"`
	Revision       string    `json:"revision,omitempty"`
	Ready          bool      `json:"ready"`
	ReadyError     string    `json:"ready_error,omitempty"`
	Sessions       int       `json:"sessions"`
	ActionsPending int       `json:"actions_pending"`
	ActionsFailed  int       `json:"actions_failed"`
}

func (s *site) status(ctx context.Context) Status {
	st := Status{
		Version:       s.src.Version,
		Started:       s.src.Started,
		UptimeSeconds: int64(time.Since(s.src.Started).Seconds()),
		ConfigHash:    s.src.Config().Hash,
		Revision:      s.src.Revision,
		Ready:         true,
		Sessions:      len(s.src.Sessions()),
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.src.Ready(ctx); err != nil {
		st.Ready, st.ReadyError = false, err.Error()
	}

	if pending, failed, err := s.src.Actions(); err == nil {
		st.ActionsPending, st.ActionsFailed = len(pending), len(failed)
	}
	return st
}

func (s *site) apiStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.status(r.Context()))
}

func (s *site) apiSessions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.src.Sessions())
}

func (s *site) apiEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, audit.Recent(eventFilter(r)))
}

// actionsList is the body of /api/v1/actions.
type actionsList struct {
	Pending []actions.Job `json:"pending"`
	Failed  []actions.Job `json:"failed"`
}

func (s *site) apiActions(w http.ResponseWriter, _ *http.Request) {
	pending, failed, err := s.src.Actions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, actionsList{Pending: pending, Failed: failed})
}

// apiConfig returns the config file as written. Secrets in it are only
// ${...} references, so it is safe to show.
func (s *site) apiConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(s.src.Config().Raw)
}

// eventFilter reads ?event=, ?account=, ?result= and ?limit= (default 200,
// at most 2000).
func eventFilter(r *http.Request) audit.Filter {
	q := r.URL.Query()
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 {
		limit = 200
	}
	return audit.Filter{
		Event:   q.Get("event"),
		Account: q.Get("account"),
		Result:  q.Get("result"),
		Limit:   min(limit, 2000),
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
