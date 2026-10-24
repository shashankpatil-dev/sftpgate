// Package actions runs a folder's post-upload steps (PGP, copy, webhook)
// after a file has been saved to the bucket.
//
// Every saved file becomes a job: a small JSON file in the queue folder on
// disk. Workers pick jobs up, run the steps, and delete the job file when
// everything worked. A failed job is retried later with a growing wait, and
// after max_attempts it is moved to the queue's "failed" folder. Because the
// queue lives on disk, jobs survive a restart.
package actions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/metrics"
	"github.com/shashankpatil-dev/sftpgate/internal/storage"
	"github.com/shashankpatil-dev/sftpgate/internal/tracing"
	"github.com/shashankpatil-dev/sftpgate/internal/vfs"
)

// maxBackoff is the longest wait between two attempts of one job.
const maxBackoff = time.Hour

// Job is one saved file whose folder has post-upload steps.
type Job struct {
	ID        string    `json:"id"`
	Account   string    `json:"account"`
	Folder    string    `json:"folder"`
	Storage   string    `json:"storage"`
	Key       string    `json:"key"`
	Filename  string    `json:"filename"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Uploaded  time.Time `json:"uploaded"`
	Attempt   int       `json:"attempt"`
	NextAt    time.Time `json:"next_at"`
	LastError string    `json:"last_error,omitempty"`

	// TraceParent points at the upload's trace, so the job's own trace can
	// link back to it.
	TraceParent string `json:"traceparent,omitempty"`
}

// Runner owns the queue folder and the workers that process it.
type Runner struct {
	// Dir holds pending jobs; Dir/failed holds the jobs that gave up.
	Dir string

	// WorkDir holds temporary copies of files while their steps run.
	WorkDir string

	Config  func() *config.Config
	Buckets *storage.Registry
	Client  *http.Client

	// Now returns the current time. Tests replace it to skip waiting.
	Now func() time.Time

	wake    chan struct{}
	mu      sync.Mutex
	running map[string]bool
}

// NewRunner creates the queue folders and removes temporary files left over
// from a crash. Jobs already in the queue are kept and will run.
func NewRunner(dir, workDir string, cfg func() *config.Config, buckets *storage.Registry) (*Runner, error) {
	if err := os.MkdirAll(filepath.Join(dir, "failed"), 0o700); err != nil {
		return nil, fmt.Errorf("action queue: %w", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(workDir, "work-*"))
	for _, f := range leftovers {
		_ = os.Remove(f)
	}

	return &Runner{
		Dir:     dir,
		WorkDir: workDir,
		Config:  cfg,
		Buckets: buckets,
		Client:  &http.Client{Timeout: 10 * time.Second},
		Now:     time.Now,
		wake:    make(chan struct{}, 1),
		running: map[string]bool{},
	}, nil
}

// Add queues the post-upload steps for a saved file.
func (r *Runner) Add(u vfs.Uploaded) error {
	job := Job{
		ID:       newJobID(u.Time),
		Account:  u.Account,
		Folder:   u.Folder,
		Storage:  u.Storage,
		Key:      u.Key,
		Filename: u.Filename,
		Size:     u.Size,
		SHA256:   u.SHA256,
		Uploaded: u.Time,
		NextAt:   u.Time,

		TraceParent: u.TraceParent,
	}
	if err := r.save(job, r.jobPath(job.ID)); err != nil {
		return err
	}
	metrics.Actions.WithLabelValues("queue", audit.OK).Inc()

	// Wake the dispatcher so the job starts now, not on the next tick.
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}

// Run processes the queue until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	jobs := make(chan Job)
	var wg sync.WaitGroup
	for range r.Config().Actions.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				r.process(ctx, job)
			}
		}()
	}

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		r.dispatch(ctx, jobs)
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		case <-tick.C:
		case <-r.wake:
		}
	}
}

// dispatch hands every job that is due to the workers.
func (r *Runner) dispatch(ctx context.Context, jobs chan<- Job) {
	due := r.dueJobs()
	for i, job := range due {
		select {
		case jobs <- job:
		case <-ctx.Done():
			for _, left := range due[i:] {
				r.release(left.ID)
			}
			return
		}
	}
}

// RunDue runs every job that is due right now, one after another, and
// returns when they are done. Tests use it to drive the queue step by step.
func (r *Runner) RunDue(ctx context.Context) {
	for _, job := range r.dueJobs() {
		r.process(ctx, job)
	}
}

// dueJobs returns the jobs whose time has come, marked as running.
// It also updates the queue size metrics.
func (r *Runner) dueJobs() []Job {
	pending, err := filepath.Glob(filepath.Join(r.Dir, "*.json"))
	if err != nil {
		slog.Error("cannot read the action queue", "dir", r.Dir, "err", err)
		return nil
	}
	failed, _ := filepath.Glob(filepath.Join(r.Dir, "failed", "*.json"))
	metrics.ActionsQueue.WithLabelValues("pending").Set(float64(len(pending)))
	metrics.ActionsQueue.WithLabelValues("failed").Set(float64(len(failed)))

	now := r.Now()
	var due []Job
	for _, p := range pending {
		job, err := load(p)
		if err != nil {
			slog.Error("unreadable job file, moving it to failed", "file", p, "err", err)
			_ = os.Rename(p, filepath.Join(r.Dir, "failed", filepath.Base(p)))
			continue
		}
		if job.NextAt.After(now) || !r.claim(job.ID) {
			continue
		}
		due = append(due, job)
	}
	return due
}

// List returns the jobs waiting in the queue and the jobs that gave up,
// oldest first, for the web UI.
func (r *Runner) List() (pending, failed []Job, err error) {
	if pending, err = loadAll(filepath.Join(r.Dir, "*.json")); err != nil {
		return nil, nil, err
	}
	failed, err = loadAll(filepath.Join(r.Dir, "failed", "*.json"))
	return pending, failed, err
}

// loadAll reads every job file matching pattern. Job ids start with the
// time, so the sorted file names are already oldest first.
func loadAll(pattern string) ([]Job, error) {
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(files))
	for _, f := range files {
		if job, err := load(f); err == nil {
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

// claim marks a job as running, so it is never handed out twice.
func (r *Runner) claim(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running[id] {
		return false
	}
	r.running[id] = true
	return true
}

func (r *Runner) release(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.running, id)
}

// process runs one job and records the result.
func (r *Runner) process(ctx context.Context, job Job) {
	defer r.release(job.ID)

	// Each attempt gets its own trace, linked to the upload's trace. It may
	// run long after the upload, even after a restart.
	ctx, span := tracing.Tracer.Start(ctx, "action.run",
		trace.WithNewRoot(),
		trace.WithLinks(trace.Link{SpanContext: tracing.Extract(job.TraceParent)}),
		trace.WithAttributes(
			attribute.String("sftpgate.job", job.ID),
			attribute.String("sftpgate.account", job.Account),
			attribute.String("sftpgate.folder", job.Folder),
			attribute.Int("sftpgate.attempt", job.Attempt+1),
		))
	err := r.runSteps(ctx, job)
	tracing.End(span, err)

	ev := audit.Event{
		Event:   "action",
		Account: job.Account,
		Storage: job.Storage,
		Key:     job.Key,
		Job:     job.ID,
		TraceID: tracing.TraceID(ctx),
		Result:  audit.OK,
	}

	if err == nil {
		if err := os.Remove(r.jobPath(job.ID)); err != nil {
			slog.Error("job finished but its file could not be removed; it will run again", "job", job.ID, "err", err)
		}
		metrics.Actions.WithLabelValues("job", audit.OK).Inc()
		audit.Emit(ev)
		return
	}

	// Stop on shutdown without counting it as a failed attempt.
	if ctx.Err() != nil {
		return
	}

	job.Attempt++
	job.LastError = err.Error()
	ev.Result, ev.Error = audit.Failed, err.Error()
	metrics.Actions.WithLabelValues("job", audit.Failed).Inc()

	var permanent *permanentError
	giveUp := errors.As(err, &permanent) || job.Attempt >= r.Config().Actions.MaxAttempts
	if giveUp {
		ev.Error += " (gave up; job moved to failed)"
		r.moveToFailed(job)
	} else {
		job.NextAt = r.Now().Add(backoff(r.Config().Actions.Backoff, job.Attempt))
		if err := r.save(job, r.jobPath(job.ID)); err != nil {
			slog.Error("could not save job for retry", "job", job.ID, "err", err)
		}
	}
	audit.Emit(ev)
}

func (r *Runner) moveToFailed(job Job) {
	if err := r.save(job, filepath.Join(r.Dir, "failed", job.ID+".json")); err != nil {
		slog.Error("could not move job to failed", "job", job.ID, "err", err)
		return
	}
	_ = os.Remove(r.jobPath(job.ID))
}

// backoff returns how long to wait before attempt number n+1:
// base, 2×base, 4×base and so on, never more than maxBackoff.
func backoff(base time.Duration, attempt int) time.Duration {
	wait := base
	for i := 1; i < attempt && wait < maxBackoff; i++ {
		wait *= 2
	}
	return min(wait, maxBackoff)
}

func (r *Runner) jobPath(id string) string {
	return filepath.Join(r.Dir, id+".json")
}

// save writes a job file. It writes a temp file and renames it, so a crash
// never leaves a half-written job behind.
func (r *Runner) save(job Job, path string) error {
	b, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func load(path string) (Job, error) {
	var job Job
	b, err := os.ReadFile(path)
	if err != nil {
		return job, err
	}
	err = json.Unmarshal(b, &job)
	if err == nil && (job.ID == "" || strings.ContainsAny(job.ID, `/\`)) {
		err = errors.New("job has no valid id")
	}
	return job, err
}

// newJobID returns an id that sorts by time, like 20261024T075812Z-1a2b3c4d.
func newJobID(t time.Time) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return t.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
}

// permanentError is a failure that retrying can't fix, like a folder that
// was removed from the config.
type permanentError struct{ msg string }

func (e *permanentError) Error() string { return e.msg }

func permanent(format string, a ...any) error {
	return &permanentError{msg: fmt.Sprintf(format, a...)}
}
