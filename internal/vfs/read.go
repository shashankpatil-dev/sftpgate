package vfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
// SFTP clients ask for 32 KiB pieces by offset, many at a time and out of
// order. Instead of keeping one long stream from the bucket (which breaks
// when the client jumps around), the file is fetched in fixed windows of
// windowSize bytes. Each window is one complete, bounded range read. The
// last few windows are kept, so pieces that arrive out of order are served
// from memory.
type download struct {
	h     *Handler
	t     *target
	event audit.Event
	size  int64
	start time.Time
	span  trace.Span

	mu      sync.Mutex
	windows []*window
	sent    int64
	failed  error
}

// window is one fetched piece of the file.
type window struct {
	off  int64
	data []byte
}

const (
	// windowSize is how much of the file one storage request fetches.
	windowSize = 1 << 20

	// keptWindows is how many recent windows are kept. Memory per download
	// is at most keptWindows x windowSize, whatever the file size.
	keptWindows = 3

	// fetchTries is how many times a window is fetched before giving up.
	fetchTries = 3
)

// errShortWindow is returned when the storage keeps sending less of a
// window than asked for.
var errShortWindow = errors.New("storage sent less data than asked for")

// ReadAt returns the piece of the file at offset off. It fills p
// completely unless the end of the file is reached.
func (d *download) ReadAt(p []byte, off int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	read := 0
	for read < len(p) {
		at := off + int64(read)
		if at >= d.size {
			return read, io.EOF
		}
		w, err := d.window(at)
		if err != nil {
			d.failed = err
			return read, clientError(d.t, err)
		}
		n := copy(p[read:], w.data[at-w.off:])
		read += n
		d.sent += int64(n)
	}
	return read, nil
}

// window returns the window holding offset at, fetching it if needed.
// Windows start at multiples of windowSize, so pieces asked for out of
// order land in the same window.
func (d *download) window(at int64) (*window, error) {
	for _, w := range d.windows {
		if at >= w.off && at < w.off+int64(len(w.data)) {
			return w, nil
		}
	}

	start := at - at%windowSize
	length := min(int64(windowSize), d.size-start)
	data, err := d.fetch(start, length)
	if err != nil {
		return nil, err
	}

	w := &window{off: start, data: data}
	d.windows = append([]*window{w}, d.windows...)
	if len(d.windows) > keptWindows {
		d.windows = d.windows[:keptWindows]
	}
	return w, nil
}

// fetch reads exactly length bytes at start. A request the storage cuts
// short is tried again; data is only used when the whole window arrived.
func (d *download) fetch(start, length int64) ([]byte, error) {
	buf := make([]byte, length)
	var err error
	for try := 1; try <= fetchTries; try++ {
		var r *blob.Reader
		r, err = d.t.bucket.NewRangeReader(context.Background(), d.t.key, start, length, nil)
		if err != nil {
			return nil, err
		}
		_, err = io.ReadFull(r, buf)
		_ = r.Close()
		if err == nil {
			return buf, nil
		}
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, err
		}
		slog.Warn("storage sent a short window, fetching it again", "key", d.t.key, "at", start, "try", try)
	}
	return nil, fmt.Errorf("%w (%d tries): %w", errShortWindow, fetchTries, err)
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

	d.windows = nil

	e := d.event
	e.Size = d.sent
	e.DurationMS = time.Since(d.start).Milliseconds()

	folder := d.t.folder.Path
	metrics.TransferBytes.WithLabelValues(d.h.Account.Name, folder, "out").Add(float64(d.sent))
	d.h.BytesOut.Add(d.sent)
	metrics.TransferDuration.WithLabelValues("out").Observe(time.Since(d.start).Seconds())

	// Storage errors were already logged in ReadAt, so only record the result.
	d.h.record(e, d.t, d.failed)
	d.span.SetAttributes(attribute.Int64("sftpgate.size", d.sent))
	tracing.End(d.span, d.failed)
	return nil
}
