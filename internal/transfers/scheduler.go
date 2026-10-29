// Package transfers runs scheduled transfers: pulling new files from a
// partner's SFTP server into a storage, or pushing files from a storage to
// a partner's SFTP server.
//
// Transfers keep no state of their own. A pull skips files that are already
// in the bucket; a push either skips files the remote already has, or
// deletes or moves each file in the bucket once it is sent.
package transfers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/metrics"
	"github.com/shashankpatil-dev/sftpgate/internal/storage"
	"github.com/shashankpatil-dev/sftpgate/internal/vfs"
)

// configCheck is how often the scheduler looks for a reloaded config.
const configCheck = 2 * time.Second

// Scheduler starts each transfer at the times its schedule says.
type Scheduler struct {
	Config  func() *config.Config
	Buckets *storage.Registry

	// WorkDir holds pulled files while they are checked and uploaded.
	WorkDir string

	// Before runs before_push and before_pull actions. Transfers with those
	// hooks fail while it is nil.
	Before vfs.BeforeFunc

	// Enqueue queues after_push and after_pull actions. It may be nil, and
	// then those hooks don't run.
	Enqueue func(vfs.FileEvent) error

	// Now returns the current time. Tests replace it.
	Now func() time.Time

	mu      sync.Mutex
	running map[string]bool
}

// NewScheduler returns a scheduler for the transfers in the config.
func NewScheduler(cfg func() *config.Config, buckets *storage.Registry, workDir string) *Scheduler {
	return &Scheduler{
		Config:  cfg,
		Buckets: buckets,
		WorkDir: workDir,
		Now:     time.Now,
		running: map[string]bool{},
	}
}

// Run follows the schedule until ctx is cancelled. When the config is
// reloaded, the schedule is rebuilt from the new one.
func (s *Scheduler) Run(ctx context.Context) {
	var c *cron.Cron
	hash := ""

	tick := time.NewTicker(configCheck)
	defer tick.Stop()
	for {
		if cfg := s.Config(); cfg.Hash != hash {
			if c != nil {
				// Runs already going finish on their own.
				c.Stop()
			}
			c = s.build(ctx, cfg)
			c.Start()
			hash = cfg.Hash
		}

		select {
		case <-ctx.Done():
			<-c.Stop().Done()
			return
		case <-tick.C:
		}
	}
}

func (s *Scheduler) build(ctx context.Context, cfg *config.Config) *cron.Cron {
	c := cron.New(cron.WithLocation(cfg.Server.Location))
	for _, t := range cfg.Transfers {
		name := t.Name
		if _, err := c.AddFunc(t.Schedule, func() { _ = s.RunNow(ctx, name) }); err != nil {
			// The config check already accepted the schedule, so this can't happen.
			slog.Error("cannot schedule transfer", "transfer", name, "err", err)
		}
	}
	return c
}

// Errors from RunNow and Start.
var (
	ErrBusy       = errors.New("transfer is still running from last time")
	ErrNoTransfer = errors.New("no transfer with that name")
)

// RunNow runs one transfer right away and waits for it. A transfer never
// runs twice at the same time: a run that is due while the last one is
// still going is skipped.
func (s *Scheduler) RunNow(ctx context.Context, name string) error {
	if !s.claim(name) {
		slog.Warn("skipping transfer run", "transfer", name, "reason", ErrBusy)
		return ErrBusy
	}
	defer s.release(name)
	return s.run(ctx, name)
}

// Start runs one transfer in the background, for the admin API. It returns
// at once: ErrNoTransfer or ErrBusy, or nil when the run has started.
func (s *Scheduler) Start(ctx context.Context, name string) error {
	if find(s.Config(), name) == nil {
		return ErrNoTransfer
	}
	if !s.claim(name) {
		return ErrBusy
	}
	go func() {
		defer s.release(name)
		_ = s.run(ctx, name)
	}()
	return nil
}

func (s *Scheduler) run(ctx context.Context, name string) error {
	cfg := s.Config()
	t := find(cfg, name)
	if t == nil {
		return fmt.Errorf("%w: %q", ErrNoTransfer, name)
	}

	run := &run{s: s, cfg: cfg, t: t, ctx: ctx, started: s.Now()}
	var err error
	if t.Pull != nil {
		err = run.pull()
	} else {
		err = run.push()
	}

	result := audit.OK
	if err != nil {
		result = audit.Failed
		audit.Emit(audit.Event{Event: "transfer", Result: audit.Failed, Transfer: name, Error: err.Error()})
	}
	metrics.TransferRuns.WithLabelValues(name, result).Inc()
	slog.Info("transfer finished", "transfer", name, "moved", run.moved, "skipped", run.skipped, "failed", run.failed, "err", err)
	return err
}

func (s *Scheduler) claim(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[name] {
		return false
	}
	s.running[name] = true
	return true
}

func (s *Scheduler) release(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, name)
}

func find(cfg *config.Config, name string) *config.Transfer {
	for _, t := range cfg.Transfers {
		if t.Name == name {
			return t
		}
	}
	return nil
}
