package vfs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path"
	"sync"
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

	spoolDir := h.Config.Server.SpoolDir
	if free, err := FreeSpace(spoolDir); err == nil {
		if free < int64(h.Config.Server.MinSpoolFree) {
			return nil, h.finish(e, t, errSpoolFull)
		}
	}
	f, err := os.CreateTemp(spoolDir, "up-*")
	if err != nil {
		return nil, h.finish(e, t, err)
	}

	ctx, span := h.startSpan("sftp.upload", t)
	return &upload{h: h, t: t, event: e, file: f, max: int64(t.folder.MaxFileSize), start: time.Now(), ctx: ctx, span: span}, nil
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

	mu   sync.Mutex
	file *os.File
	size int64

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
	u.size = max(u.size, off+int64(n))
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

	e := u.event
	e.Size = u.size
	u.span.SetAttributes(attribute.Int64("sftpgate.size", u.size))

	// The client caused this (dropped connection, file too big), not the
	// storage, so the error goes back as it is.
	if u.failed != nil {
		u.h.record(e, u.t, u.failed)
		tracing.End(u.span, u.failed)
		return u.failed
	}

	_, write := tracing.Tracer.Start(u.ctx, "storage.write")
	sha, err := storage.PutFile(context.Background(), u.t.bucket, u.t.key, u.file, u.size, map[string]string{
		"sftpgate-account": u.h.Account.Name,
		"sftpgate-ip":      u.h.IP,
		"sftpgate-session": u.h.SessionID,
	})
	tracing.End(write, err)
	defer func() { tracing.End(u.span, err) }()
	e.SHA256 = sha
	e.DurationMS = time.Since(u.start).Milliseconds()
	if err != nil {
		return u.h.finish(e, u.t, err)
	}

	folder := u.t.folder.Path
	metrics.TransferBytes.WithLabelValues(u.h.Account.Name, folder, "in").Add(float64(u.size))
	u.h.BytesIn.Add(u.size)
	metrics.TransferDuration.WithLabelValues("in").Observe(time.Since(u.start).Seconds())
	u.h.record(e, u.t, nil)
	u.afterUpload(sha)
	return nil
}

// afterUpload hands the saved file to the post-upload actions, if the folder
// has any. The file is already safe in the bucket, so a failure here is
// logged and counted but not reported to the client.
func (u *upload) afterUpload(sha string) {
	if len(u.t.folder.OnUpload) == 0 || u.h.AfterUpload == nil {
		return
	}
	err := u.h.AfterUpload(Uploaded{
		Account:  u.h.Account.Name,
		Folder:   u.t.folder.Path,
		Storage:  u.t.folder.Storage,
		Key:      u.t.key,
		Filename: path.Base(u.t.rel),
		Size:     u.size,
		SHA256:   sha,
		Time:     time.Now(),

		TraceParent: tracing.Inject(u.ctx),
	})
	if err != nil {
		slog.Error("could not queue post-upload actions", "key", u.t.key, "err", err)
		metrics.Actions.WithLabelValues("queue", "failed").Inc()
	}
}

// discardSpool closes and deletes the spool file. A file left behind would
// waste disk space, so a failure is logged.
func (u *upload) discardSpool() {
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
