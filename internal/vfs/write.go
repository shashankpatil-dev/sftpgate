package vfs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sys/unix"

	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/metrics"
	"github.com/shashankpatil-dev/sftpgate/internal/storage"
	"github.com/shashankpatil-dev/sftpgate/internal/tracing"
)

// errSpoolFull is returned when the spool disk is almost full.
var errSpoolFull = errors.New("server is out of upload space, try again later")

// Filewrite starts an upload.
//
// The file is first written to a temp file on local disk (the spool). It is
// sent to the bucket only when the client closes it, so nobody downstream
// ever sees a half-written file.
func (h *Handler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	t, err := h.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}
	e := h.event("upload", t)

	if !t.allowed(config.PermUpload) {
		return nil, h.finish(e, t, sftp.ErrSSHFxPermissionDenied)
	}
	// Appending would mean reading the old object back; clients rarely need it.
	if r.Pflags().Append {
		return nil, h.finish(e, t, sftp.ErrSSHFxOpUnsupported)
	}
	if !matchesPatterns(t.folder, path.Base(t.rel)) {
		return nil, h.finish(e, t, sftp.ErrSSHFxPermissionDenied)
	}

	found, err := exists(r.Context(), t)
	if err != nil {
		return nil, h.finish(e, t, err)
	}
	if found && !t.folder.Perm.Has(config.PermOverwrite) {
		return nil, h.finish(e, t, sftp.ErrSSHFxPermissionDenied)
	}

	u := &upload{h: h, t: t, event: e, max: int64(t.folder.MaxFileSize), dir: h.Config.Server.SpoolDir, start: time.Now()}
	if err := reserve(u, int64(h.Config.Server.MinSpoolFree)); err != nil {
		return nil, h.finish(e, t, err)
	}
	if u.file, err = os.CreateTemp(u.dir, "up-*"); err != nil {
		release(u)
		return nil, h.finish(e, t, err)
	}

	u.ctx, u.span = h.startSpan("sftp.upload", t)
	return u, nil
}

// upload is one file being received from the client.
type upload struct {
	h     *Handler
	t     *target
	event audit.Event
	start time.Time

	// ctx and span trace this upload.
	ctx  context.Context
	span trace.Span

	// max is the size limit in bytes; 0 means no limit.
	max int64

	// dir is the spool folder the file is written to.
	dir string

	mu   sync.Mutex
	file *os.File

	// size is how far into the file the client has written. It is atomic
	// so the space check can read it without taking mu.
	size atomic.Int64

	// failed is set when the upload must not be saved, for example because
	// the client disconnected or the file got too big.
	failed error
}

// WriteAt stores one chunk in the spool file. pkg/sftp may call it from
// several goroutines and the chunks may arrive out of order.
func (u *upload) WriteAt(p []byte, off int64) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.failed != nil {
		return 0, u.failed
	}
	if u.max > 0 && off+int64(len(p)) > u.max {
		u.failed = errTooLarge
		return 0, errTooLarge
	}

	n, err := u.file.WriteAt(p, off)
	if err != nil {
		u.failed = err
	}
	u.size.Store(max(u.size.Load(), off+int64(n)))
	return n, err
}

// TransferError is called by pkg/sftp when the session ends while the file
// is still open. The upload is then dropped instead of saved.
func (u *upload) TransferError(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.failed = err
}

// Close sends the finished file to the bucket, then removes the spool file.
func (u *upload) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	defer u.discardSpool()

	size := u.size.Load()
	e := u.event
	e.Size = size
	u.span.SetAttributes(attribute.Int64("sftpgate.size", size))

	// The client caused this (dropped connection, file too big), not the
	// storage, so the error goes back as it is.
	if u.failed != nil {
		u.h.record(e, u.t, u.failed)
		tracing.End(u.span, u.failed)
		return u.failed
	}
	if err := u.before(); err != nil {
		tracing.End(u.span, err)
		return u.h.finish(e, u.t, err)
	}

	_, write := tracing.Tracer.Start(u.ctx, "storage.write")
	sha, err := storage.PutFile(context.Background(), u.t.bucket, u.t.key, u.file, size, storage.PutOptions{
		Metadata: map[string]string{
			"sftpgate-account": u.h.Account.Name,
			"sftpgate-ip":      u.h.IP,
			"sftpgate-session": u.h.SessionID,
		},
		// Without overwrite permission, the bucket itself refuses to replace
		// a file. The check at open isn't enough: two uploads of the same name
		// can both pass it before either one is saved.
		IfNotExist: !u.t.folder.Perm.Has(config.PermOverwrite),
	})
	tracing.End(write, err)
	defer func() { tracing.End(u.span, err) }()
	e.SHA256 = sha
	e.DurationMS = time.Since(u.start).Milliseconds()
	if err != nil {
		return u.h.finish(e, u.t, err)
	}

	folder := u.t.folder.Path
	metrics.TransferBytes.WithLabelValues(u.h.Account.Name, folder, "in").Add(float64(size))
	u.h.BytesIn.Add(size)
	metrics.TransferDuration.WithLabelValues("in").Observe(time.Since(u.start).Seconds())
	u.h.record(e, u.t, nil)
	u.h.enqueue(u.ctx, config.AfterUpload, u.t, size, sha)
	return nil
}

// before runs the folder's before_upload checks on the finished spool file.
// A failed check refuses the upload, and nothing is saved.
func (u *upload) before() error {
	steps := u.t.folder.Hooks.Steps(config.BeforeUpload)
	if len(steps) == 0 {
		return nil
	}
	// Never skip a check silently: without a way to run it, refuse the file.
	if u.h.Before == nil {
		return errors.New("before_upload checks can't run")
	}
	_, _, cleanup, err := u.h.Before(u.ctx, steps, u.file.Name(), path.Base(u.t.rel))
	if cleanup != nil {
		cleanup()
	}
	return err
}

// discardSpool closes and deletes the spool file. A file left behind would
// waste disk space, so a failure is logged.
func (u *upload) discardSpool() {
	release(u)
	_ = u.file.Close()
	if err := os.Remove(u.file.Name()); err != nil {
		slog.Warn("could not remove spool file", "file", u.file.Name(), "err", err)
	}
}

// FreeSpace returns the free bytes on the disk that holds dir, and
// publishes the number as the spool free-space metric.
func FreeSpace(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	free := int64(st.Bavail) * st.Bsize
	metrics.SpoolFree.Set(float64(free))
	return free, nil
}
