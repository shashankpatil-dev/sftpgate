// Package web serves the HTTP endpoints: /metrics for Prometheus and
// /healthz and /readyz for health checks.
package web

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Handler returns the HTTP routes. ready reports whether the storages can
// be reached; it decides the answer of /readyz.
//
// Only GET routes exist, so nothing here can change the server.
func Handler(ready func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})

	return mux
}
