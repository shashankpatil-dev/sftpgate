package vfs

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"gocloud.dev/blob"

	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/metrics"
	"github.com/shashankpatil-dev/sftpgate/internal/tracing"
)

// Fileread starts a download. The file is streamed straight from the bucket.
func (h *Handler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	t, err := h.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}
	e := h.event("download", t)

	if !t.allowed(config.PermDownload) {
		return nil, h.finish(e, t, sftp.ErrSSHFxPermissionDenied)
	}
	attrs, err := t.bucket.Attributes(r.Context(), t.key)
	if err != nil {
		return nil, h.finish(e, t, err)
	}

	_, span := h.startSpan("sftp.download", t)
	return &download{h: h, t: t, event: e, size: attrs.Size, start: time.Now(), span: span}, nil
}

// download is one file being sent to the client.
//
// Reading from a bucket is a stream, but SFTP asks for chunks by offset.
// We keep one stream open and reuse it while the chunks come in order, and
// open a new one only when the client jumps to another offset.
type download struct {
	h     *Handler
	t     *target
	event audit.Event
	size  int64
	start time.Time
	span  trace.Span

	mu     sync.Mutex
	reader *blob.Reader
	pos    int64
	sent   int64
	failed error
}

// ReadAt returns the chunk of the file at offset off.
func (d *download) ReadAt(p []byte, off int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if off >= d.size {
		return 0, io.EOF
	}

	if d.reader == nil || off != d.pos {
		if d.reader != nil {
			_ = d.reader.Close()
		}
		r, err := d.t.bucket.NewRangeReader(context.Background(), d.t.key, off, -1, nil)
		if err != nil {
			d.reader = nil
			d.failed = err
			return 0, clientError(d.t, err)
		}
		d.reader, d.pos = r, off
	}

	n, err := io.ReadFull(d.reader, p)
	d.pos += int64(n)
	d.sent += int64(n)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = io.EOF
	}
	if err != nil && !errors.Is(err, io.EOF) {
		d.failed = err
		return n, clientError(d.t, err)
	}
	return n, err
}

// TransferError is called by pkg/sftp when the session ends mid-download.
func (d *download) TransferError(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failed = err
}

// Close ends the download and writes the audit record.
func (d *download) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.reader != nil {
		_ = d.reader.Close()
	}

	e := d.event
	e.Size = d.sent
	e.DurationMS = time.Since(d.start).Milliseconds()

	folder := d.t.folder.Path
	metrics.TransferBytes.WithLabelValues(d.h.Account.Name, folder, "out").Add(float64(d.sent))
	metrics.TransferDuration.WithLabelValues("out").Observe(time.Since(d.start).Seconds())

	// Storage errors were already logged in ReadAt, so only record the result.
	d.h.record(e, d.t, d.failed)
	d.span.SetAttributes(attribute.Int64("sftpgate.size", d.sent))
	tracing.End(d.span, d.failed)
	return nil
}
