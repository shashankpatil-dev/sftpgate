// Package web serves the HTTP endpoints:
//
//   - /metrics for Prometheus, and /healthz and /readyz for health checks
//   - /api/v1/... a read-only JSON API, off until http.tokens is set
//   - two admin calls that change something (run a transfer now, retry
//     failed actions), each off until http.admin allows it
//
// Config changes still only go through git: nothing here edits the config.
package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/shashankpatil-dev/sftpgate/internal/actions"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/server"
)

// Source is where the API gets its data.
type Source struct {
	Version string
	Started time.Time

	// Revision returns the git commit of the running config, or "".
	Revision func() string

	Config   func() *config.Config
	Ready    func(context.Context) error
	Sessions func() []server.SessionInfo
	Actions  func() (pending, failed []actions.Job, err error)

	// RunTransfer starts a transfer in the background. It is nil when
	// scheduled transfers run on another replica.
	RunTransfer func(name string) error

	// RetryActions moves failed jobs back into the queue: the one with
	// this id, or all when id is "". It returns how many.
	RetryActions func(id string) (int, error)
}

type site struct {
	src Source
}

// Handler returns all HTTP routes.
func Handler(src Source) http.Handler {
	s := &site{src: src}
	mux := http.NewServeMux()

	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", s.readyz)

	mux.HandleFunc("GET /api/v1/status", s.protect(s.apiStatus))
	mux.HandleFunc("GET /api/v1/sessions", s.protect(s.apiSessions))
	mux.HandleFunc("GET /api/v1/events", s.protect(s.apiEvents))
	mux.HandleFunc("GET /api/v1/actions", s.protect(s.apiActions))
	mux.HandleFunc("GET /api/v1/config", s.protect(s.apiConfig))

	mux.HandleFunc("POST /api/v1/transfers/{name}/run", s.admin(config.RunTransfer, s.apiRunTransfer))
	mux.HandleFunc("POST /api/v1/actions/retry", s.admin(config.RetryActions, s.apiRetryActions))

	return mux
}

func (s *site) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.src.Ready(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}

// protect lets a request through only with a valid read-only token, sent
// as "Bearer <token>" or as the password of a Basic login. With no tokens
// configured, the API doesn't exist.
func (s *site) protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokens := s.src.Config().HTTP.Tokens
		if len(tokens) == 0 {
			http.NotFound(w, r)
			return
		}
		safeHeaders(w)
		if !validToken(requestToken(r), tokens) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="SFTPgate"`)
			http.Error(w, "a valid token is required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func safeHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// requestToken reads the token from a Bearer header or a Basic login.
func requestToken(r *http.Request) string {
	if bearer, ok := bearerToken(r); ok {
		return bearer
	}
	_, password, _ := r.BasicAuth()
	return password
}

func bearerToken(r *http.Request) (string, bool) {
	return strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// validToken compares in constant time. Hashing first makes both sides the
// same length, so not even the token's length leaks.
func validToken(got string, tokens []string) bool {
	if got == "" {
		return false
	}
	g := sha256.Sum256([]byte(got))
	ok := 0
	for _, t := range tokens {
		want := sha256.Sum256([]byte(t))
		ok |= subtle.ConstantTimeCompare(g[:], want[:])
	}
	return ok == 1
}
