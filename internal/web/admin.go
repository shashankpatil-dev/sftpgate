package web

import (
	"errors"
	"net"
	"net/http"

	"github.com/shashankpatil-dev/sftpgate/internal/actions"
	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/transfers"
)

// admin guards a call that changes something. It exists only while
// http.admin allows op, and it takes only an admin token, only as a Bearer
// header: browsers never send that on their own, so another web page can't
// make a logged-in browser call it. Every call is audited.
func (s *site) admin(op string, next func(*http.Request) (int, any, audit.Event)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := s.src.Config().HTTP.Admin
		if !a.Allows(op) {
			http.NotFound(w, r)
			return
		}
		safeHeaders(w)

		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		bearer, _ := bearerToken(r)
		if !validToken(bearer, a.Tokens) {
			audit.Emit(audit.Event{Event: "admin", Method: op, Result: audit.Denied, IP: ip, Error: "no valid admin token"})
			w.Header().Set("WWW-Authenticate", `Bearer realm="SFTPgate admin"`)
			http.Error(w, "a valid admin token is required", http.StatusUnauthorized)
			return
		}

		code, body, ev := next(r)
		ev.Event, ev.Method, ev.IP = "admin", op, ip
		if ev.Result == "" {
			ev.Result = audit.OK
		}
		audit.Emit(ev)
		w.WriteHeader(code)
		writeJSON(w, body)
	}
}

type apiError struct {
	Error string `json:"error"`
}

func (s *site) apiRunTransfer(r *http.Request) (int, any, audit.Event) {
	name := r.PathValue("name")
	ev := audit.Event{Transfer: name}
	if s.src.RunTransfer == nil {
		ev.Result, ev.Error = audit.Failed, "transfers don't run on this replica"
		return http.StatusConflict, apiError{"scheduled transfers don't run on this replica; call the one named in SFTPGATE_TRANSFERS_ONLY_ON"}, ev
	}
	switch err := s.src.RunTransfer(name); {
	case errors.Is(err, transfers.ErrNoTransfer):
		ev.Result, ev.Error = audit.Failed, err.Error()
		return http.StatusNotFound, apiError{err.Error()}, ev
	case errors.Is(err, transfers.ErrBusy), errors.Is(err, transfers.ErrDisabled):
		ev.Result, ev.Error = audit.Failed, err.Error()
		return http.StatusConflict, apiError{err.Error()}, ev
	case err != nil:
		ev.Result, ev.Error = audit.Failed, err.Error()
		return http.StatusInternalServerError, apiError{err.Error()}, ev
	}
	// The run's own result is in the "transfer" audit event and the metrics.
	return http.StatusAccepted, map[string]string{"transfer": name, "status": "started"}, ev
}

// apiRetryActions retries the failed job ?job=<id>, or every failed job.
// Failed jobs live on each pod's own disk, so this retries this pod's.
func (s *site) apiRetryActions(r *http.Request) (int, any, audit.Event) {
	id := r.URL.Query().Get("job")
	ev := audit.Event{Job: id}
	n, err := s.src.RetryActions(id)
	switch {
	case errors.Is(err, actions.ErrNoJob):
		ev.Result, ev.Error = audit.Failed, err.Error()
		return http.StatusNotFound, apiError{err.Error()}, ev
	case err != nil:
		ev.Result, ev.Error = audit.Failed, err.Error()
		return http.StatusInternalServerError, apiError{err.Error()}, ev
	}
	return http.StatusOK, map[string]int{"retried": n}, ev
}
