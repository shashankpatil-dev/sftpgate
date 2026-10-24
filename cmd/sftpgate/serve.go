package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	// Registers /debug/pprof on the default mux, served only when SFTPGATE_PPROF is set.
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/shashankpatil-dev/sftpgate/internal/actions"
	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/metrics"
	"github.com/shashankpatil-dev/sftpgate/internal/server"
	"github.com/shashankpatil-dev/sftpgate/internal/storage"
	"github.com/shashankpatil-dev/sftpgate/internal/tracing"
	"github.com/shashankpatil-dev/sftpgate/internal/transfers"
	"github.com/shashankpatil-dev/sftpgate/internal/vfs"
	"github.com/shashankpatil-dev/sftpgate/internal/web"
)

// shutdownGrace is how long open sessions get to finish on shutdown.
const shutdownGrace = 30 * time.Second

// serve runs the gateway until it gets SIGTERM or SIGINT.
func serve(path string) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load(path, config.Options{})
	if err != nil {
		return fmt.Errorf("config %s:\n%w", path, err)
	}
	if err := prepareSpool(cfg.Server.SpoolDir); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	stopTracing, err := tracing.Init(ctx, version)
	if err != nil {
		return fmt.Errorf("tracing: %w", err)
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = stopTracing(flushCtx)
	}()

	buckets := storage.NewRegistry()
	if err := buckets.Prepare(ctx, cfg); err != nil {
		return err
	}

	r := &reloader{path: path, buckets: buckets, loaded: config.SourceHash(cfg.Raw)}
	r.current.Store(cfg)
	srv := server.New(r.current.Load, buckets)
	r.server = srv
	r.setConfigInfo(cfg)
	metrics.Prepare(cfg)

	runner, err := actions.NewRunner(filepath.Join(cfg.Server.SpoolDir, "queue"), cfg.Server.SpoolDir, r.current.Load, buckets)
	if err != nil {
		return err
	}
	srv.AfterUpload = runner.Add

	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr: cfg.HTTP.Listen,
		Handler: web.Handler(web.Source{
			Version:  version,
			Started:  time.Now(),
			Revision: r.currentRevision,
			Config:   r.current.Load,
			Ready:    func(ctx context.Context) error { return buckets.Ping(ctx, r.current.Load()) },
			Sessions: srv.Sessions,
			Actions:  runner.List,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server stopped", "err", err)
		}
	}()
	go r.run(ctx, cfg.Server.ReloadInterval)
	go watchSpool(ctx, cfg.Server.SpoolDir)
	go runner.Run(ctx)

	host, _ := os.Hostname()
	if transfers.EnabledHere(os.Getenv, host) {
		go transfers.NewScheduler(r.current.Load, buckets, cfg.Server.SpoolDir).Run(ctx)
	} else if len(cfg.Transfers) > 0 {
		slog.Info("scheduled transfers run on another replica", "host", host)
	}

	setMemoryLimit()
	startProfiler()
	slog.Info("sftpgate started", "version", version, "sftp", cfg.Server.Listen, "http", cfg.HTTP.Listen, "config", cfg.Hash)
	err = srv.Serve(ctx, ln, shutdownGrace)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	slog.Info("sftpgate stopped")
	return err
}

// setMemoryLimit tells Go to stay under 90% of the container's memory
// limit, so it cleans up memory before the container is killed for using
// too much. GOMEMLIMIT, when set, wins. Outside a container nothing changes.
func setMemoryLimit() {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	// cgroup v2, used by Docker and Kubernetes today. "max" means no limit.
	b, err := os.ReadFile("/sys/fs/cgroup/memory.max")
	if err != nil {
		return
	}
	limit, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || limit <= 0 {
		return
	}
	debug.SetMemoryLimit(limit / 10 * 9)
	slog.Info("memory limit set from the container", "limit_mb", limit/10*9/1e6)
}

// startProfiler serves Go's profiling pages (/debug/pprof) when
// SFTPGATE_PPROF is set, like "127.0.0.1:6060". It is off by default and
// meant for finding memory or CPU problems; keep it on localhost.
func startProfiler() {
	addr := os.Getenv("SFTPGATE_PPROF")
	if addr == "" {
		return
	}
	go func() {
		slog.Warn("profiling endpoint is on", "addr", addr)
		srv := &http.Server{Addr: addr, Handler: http.DefaultServeMux, ReadHeaderTimeout: 10 * time.Second}
		if err := srv.ListenAndServe(); err != nil {
			slog.Error("profiling endpoint stopped", "err", err)
		}
	}()
}

// prepareSpool makes sure the upload spool folder exists and is private, and
// removes uploads left over from a crash. Those were never finished, so
// they must not be saved.
func prepareSpool(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("spool dir: %w", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "up-*"))
	pulled, _ := filepath.Glob(filepath.Join(dir, "xfer-*"))
	leftovers = append(leftovers, pulled...)
	for _, f := range leftovers {
		_ = os.Remove(f)
	}
	if len(leftovers) > 0 {
		slog.Warn("removed unfinished uploads from a previous run", "count", len(leftovers))
	}
	return nil
}

// watchSpool keeps the spool free-space metric fresh, so an alert can warn
// before the disk fills up, even when no uploads are happening.
func watchSpool(ctx context.Context, dir string) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		if _, err := vfs.FreeSpace(dir); err != nil {
			slog.Warn("cannot read spool free space", "dir", dir, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// reloader watches the config file and swaps in new versions.
type reloader struct {
	path    string
	buckets *storage.Registry
	server  *server.Server
	current atomic.Pointer[config.Config]

	// loaded is the source hash (config plus referenced files) of the
	// running config, and rejected the one of the last file that failed,
	// so a broken file is reported once instead of on every check.
	loaded   string
	rejected string

	// revision is the git commit of the running config, when known.
	revision atomic.Value
}

// run checks the file every interval, and right away on SIGHUP.
// SIGHUP reloads even if the file didn't change, which picks up rotated
// secrets behind ${env:} and ${file:} references.
func (r *reloader) run(ctx context.Context, interval time.Duration) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	tick := time.NewTicker(interval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.reload(ctx, false)
		case <-hup:
			r.reload(ctx, true)
		}
	}
}

// reload loads the file and, if it is valid, makes it the live config.
// A bad file is rejected and the old config keeps running.
func (r *reloader) reload(ctx context.Context, force bool) {
	old := r.current.Load()

	raw, err := os.ReadFile(r.path)
	if err != nil {
		r.failed(old, err)
		return
	}
	hash := config.SourceHash(raw)
	if !force && (hash == r.loaded || hash == r.rejected) {
		return
	}

	cfg, err := config.Parse(raw, config.Options{})
	if err == nil {
		err = r.buckets.Prepare(ctx, cfg)
	}
	if err != nil {
		r.rejected = hash
		r.failed(old, err)
		return
	}

	r.rejected = ""
	r.loaded = hash
	r.current.Store(cfg)
	r.server.CloseChanged(cfg)
	r.setConfigInfo(cfg)
	metrics.Prepare(cfg)
	metrics.ConfigReloads.WithLabelValues(audit.OK).Inc()
	audit.Emit(audit.Event{Event: "config_reload", Result: audit.OK, Config: cfg.Hash})
	slog.Info("config reloaded", "config", cfg.Hash, "revision", r.currentRevision())
}

func (r *reloader) failed(old *config.Config, err error) {
	metrics.ConfigReloads.WithLabelValues("error").Inc()
	audit.Emit(audit.Event{Event: "config_reload", Result: audit.Failed, Error: err.Error(), Config: old.Hash})
	slog.Error("config reload rejected, keeping the old config", "err", err)
}

// currentRevision returns the git commit of the running config, or "".
func (r *reloader) currentRevision() string {
	rev, _ := r.revision.Load().(string)
	return rev
}

// setConfigInfo publishes which config, and which git commit, is running.
func (r *reloader) setConfigInfo(cfg *config.Config) {
	rev := config.Revision(r.path)
	r.revision.Store(rev)
	metrics.ConfigInfo.Reset()
	metrics.ConfigInfo.WithLabelValues(cfg.Hash, rev).Set(1)
}
