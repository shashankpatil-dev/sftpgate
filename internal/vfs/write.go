package vfs

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"gocloud.dev/blob"
	"golang.org/x/sys/unix"

	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/metrics"
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
	if free, err := freeSpace(spoolDir); err == nil {
		metrics.SpoolFree.Set(float64(free))
		if free < int64(h.Config.Server.MinSpoolFree) {
			return nil, h.finish(e, t, errSpoolFull)
		}
	}
	f, err := os.CreateTemp(spoolDir, "up-*")
	if err != nil {
		return nil, h.finish(e, t, err)
	}

	return &upload{h: h, t: t, event: e, file: f, max: int64(t.folder.MaxFileSize), start: time.Now()}, nil
}

// upload is one file being received from the client.
type upload struct {
	h     *Handler
	t     *target
	event audit.Event
	start time.Time

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
	// The client caused this (dropped connection, file too big), not the
	// storage, so the error goes back as it is.
	if u.failed != nil {
		u.h.record(e, u.t, u.failed)
		return u.failed
	}

	// Pass 1 reads the local file to get its checksums. Pass 2 sends it.
	// The bucket checks the MD5, so a corrupted transfer is never saved.
	sha, md5sum, err := checksums(io.NewSectionReader(u.file, 0, u.size))
	if err != nil {
		return u.h.finish(e, u.t, err)
	}
	e.SHA256 = hex.EncodeToString(sha)

	err = u.send(md5sum, hex.EncodeToString(sha))
	e.DurationMS = time.Since(u.start).Milliseconds()
	if err == nil {
		folder := u.t.folder.Path
		metrics.TransferBytes.WithLabelValues(u.h.Account.Name, folder, "in").Add(float64(u.size))
		metrics.TransferDuration.WithLabelValues("in").Observe(time.Since(u.start).Seconds())
	}
	return u.h.finish(e, u.t, err)
}

// send streams the spool file to the bucket. The object only appears in the
// bucket if everything worked.
func (u *upload) send(md5sum []byte, sha string) error {
	// Cancelling the context before Close tells the bucket to throw the
	// write away instead of saving it.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := u.t.bucket.NewWriter(ctx, u.t.key, &blob.WriterOptions{
		ContentMD5: md5sum,
		Metadata: map[string]string{
			"sftpgate-account": u.h.Account.Name,
			"sftpgate-ip":      u.h.IP,
			"sftpgate-session": u.h.SessionID,
			"sftpgate-sha256":  sha,
		},
	})
	if err != nil {
		return err
	}

	if _, err := io.Copy(w, io.NewSectionReader(u.file, 0, u.size)); err != nil {
		cancel()
		_ = w.Close()
		return err
	}
	return w.Close()
}

// discardSpool closes and deletes the spool file. A file left behind would
// waste disk space, so a failure is logged.
func (u *upload) discardSpool() {
	_ = u.file.Close()
	if err := os.Remove(u.file.Name()); err != nil {
		slog.Warn("could not remove spool file", "file", u.file.Name(), "err", err)
	}
}

func checksums(r io.Reader) (sha, md5sum []byte, err error) {
	s, m := sha256.New(), md5.New()
	if _, err := io.Copy(io.MultiWriter(s, m), r); err != nil {
		return nil, nil, err
	}
	return s.Sum(nil), m.Sum(nil), nil
}

// freeSpace returns the free bytes on the disk that holds dir.
func freeSpace(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * st.Bsize, nil
}
