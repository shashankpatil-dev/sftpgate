// Package web serves the HTTP endpoints:
//
//   - /metrics for Prometheus, and /healthz and /readyz for health checks
//   - /api/v1/... a read-only JSON API
//   - /ui/... a read-only web page that shows the same data
//
// The API and UI are off until http.tokens is set in the config. Only GET
// routes exist, so nothing here can change the server: every change goes
// through git.
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

// Source is where the API and UI get their data.
type Source struct {
	Version string
	Started time.Time

	// Revision is the git commit of the config, if known (SFTPGATE_REVISION).
	Revision string

	Config   func() *config.Config
	Ready    func(context.Context) error
	Sessions func() []server.SessionInfo
	Actions  func() (pending, failed []actions.Job, err error)
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

	mux.HandleFunc("GET /ui/{$}", s.protect(s.uiOverview))
	mux.HandleFunc("GET /ui/events", s.protect(s.uiEvents))
	mux.HandleFunc("GET /ui/actions", s.protect(s.uiActions))
	mux.HandleFunc("GET /ui/config", s.protect(s.uiConfig))
	mux.HandleFunc("GET /ui/style.css", s.protect(serveCSS))
	mux.Handle("GET /{$}", http.RedirectHandler("/ui/", http.StatusFound))

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

// protect lets a request through only with a valid token. Browsers send it
// as the password of the login prompt; scripts can send "Bearer <token>".
// With no tokens configured, the API and UI don't exist.
func (s *site) protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokens := s.src.Config().HTTP.Tokens
		if len(tokens) == 0 {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")

		if !validToken(requestToken(r), tokens) {
			w.Header().Set("WWW-Authenticate", `Basic realm="SFTPgate", charset="UTF-8"`)
			http.Error(w, "a valid token is required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// requestToken reads the token from a Bearer header or a Basic login.
func requestToken(r *http.Request) string {
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return bearer
	}
	_, password, _ := r.BasicAuth()
	return password
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
