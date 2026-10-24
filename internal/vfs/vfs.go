// Package vfs is the virtual filesystem an SFTP client sees.
//
// Each account sees only its own folders, like /inbound and /outbound. Every
// folder is a prefix inside a bucket. The client never touches the real disk
// of the server, and every request is checked against the folder's permissions.
package vfs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/metrics"
	"github.com/shashankpatil-dev/sftpgate/internal/storage"
	"github.com/shashankpatil-dev/sftpgate/internal/tracing"
)

// Handler serves the SFTP requests of one session.
type Handler struct {
	// Config is the config the session logged in with. It stays the same for
	// the whole session, even if the file is reloaded.
	Config    *config.Config
	Account   *config.Account
	Buckets   *storage.Registry
	SessionID string
	IP        string

	// AfterUpload is called for every file saved into a folder that has
	// on_upload actions. It may be nil.
	AfterUpload func(Uploaded) error

	// Ctx carries the session's trace span. File operations become child
	// spans of it. It may be nil when tracing isn't used.
	Ctx context.Context

	// BytesIn and BytesOut count the bytes of finished uploads and downloads.
	BytesIn  atomic.Int64
	BytesOut atomic.Int64
}

// Uploaded describes a file that was just saved to a bucket.
type Uploaded struct {
	Account  string
	Folder   string
	Storage  string
	Key      string
	Filename string
	Size     int64
	SHA256   string
	Time     time.Time

	// TraceParent links the post-upload actions to the upload's trace.
	TraceParent string
}

// Handlers returns the handler wired up the way pkg/sftp expects.
func (h *Handler) Handlers() sftp.Handlers {
	return sftp.Handlers{FileGet: h, FilePut: h, FileCmd: h, FileList: h}
}

// target is where an SFTP path points to.
type target struct {
	// path is the cleaned SFTP path, like /inbound/a.csv.
	path string

	// folder is nil for the root "/", which only lists the folders.
	folder *config.Folder

	// rel is the part of the path inside the folder; "" is the folder itself.
	rel string

	// key is the object key in the bucket: folder prefix + rel.
	key    string
	bucket *blob.Bucket
}

// resolve turns an SFTP path into a target.
//
// path.Clean runs on an absolute path, so ".." can never climb above "/",
// and the key is always built from the folder's own prefix.
func (h *Handler) resolve(p string) (*target, error) {
	clean := path.Clean("/" + p)
	if clean == "/" {
		return &target{path: clean}, nil
	}

	first, rest, _ := strings.Cut(clean[1:], "/")
	f := h.Account.Folder("/" + first)
	if f == nil {
		return nil, sftp.ErrSSHFxNoSuchFile
	}

	return &target{
		path:   clean,
		folder: f,
		rel:    rest,
		key:    f.Prefix + rest,
		bucket: h.Buckets.Bucket(h.Config, f.Storage),
	}, nil
}

// allowed reports whether the target's folder has permission p.
// The root and the folders themselves can never be changed.
func (t *target) allowed(p config.PermSet) bool {
	return t.folder != nil && t.rel != "" && t.folder.Perm.Has(p)
}

// matchesPatterns reports whether a file name is allowed in the folder.
// A folder with no patterns allows every name.
func matchesPatterns(f *config.Folder, name string) bool {
	if len(f.Patterns) == 0 {
		return true
	}
	for _, p := range f.Patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// exists reports whether an object is stored under key.
func exists(ctx context.Context, t *target) (bool, error) {
	_, err := t.bucket.Attributes(ctx, t.key)
	if gcerrors.Code(err) == gcerrors.NotFound {
		return false, nil
	}
	return err == nil, err
}

// event returns an audit event already filled with the session details.
func (h *Handler) event(name string, t *target) audit.Event {
	e := audit.Event{
		Event:   name,
		Session: h.SessionID,
		Account: h.Account.Name,
		IP:      h.IP,
		Config:  h.Config.Hash,
		TraceID: tracing.TraceID(h.ctx()),
	}
	if t != nil {
		e.Path = t.path
		e.Key = t.key
		if t.folder != nil {
			e.Storage = t.folder.Storage
		}
	}
	return e
}

// ctx returns the session context, or an empty one.
func (h *Handler) ctx() context.Context {
	if h.Ctx == nil {
		return context.Background()
	}
	return h.Ctx
}

// startSpan starts a span for one file operation, as a child of the session.
func (h *Handler) startSpan(name string, t *target) (context.Context, trace.Span) {
	ctx, span := tracing.Tracer.Start(h.ctx(), name)
	if t != nil && t.folder != nil {
		span.SetAttributes(attribute.String("sftpgate.folder", t.folder.Path))
	}
	return ctx, span
}

// finish records the result of an operation and turns the error into one
// that is safe to send to the client.
func (h *Handler) finish(e audit.Event, t *target, err error) error {
	h.record(e, t, err)
	return clientError(t, err)
}

// record writes the result of an operation to the audit log and the metrics.
// Denied requests are audited too, because they can mean someone is probing.
func (h *Handler) record(e audit.Event, t *target, err error) {
	e.Result = audit.OK
	switch {
	case errors.Is(err, sftp.ErrSSHFxPermissionDenied):
		e.Result = audit.Denied
	case err != nil:
		e.Result = audit.Failed
		e.Error = clientMessage(err)
	}
	audit.Emit(e)
	h.count(e.Event, t, e.Result)
}

func (h *Handler) count(op string, t *target, result string) {
	folder := "/"
	if t != nil && t.folder != nil {
		folder = t.folder.Path
	}
	metrics.Operations.WithLabelValues(h.Account.Name, folder, op, result).Inc()
}

// errTooLarge is returned when an upload goes over the folder's max_file_size.
var errTooLarge = errors.New("file too large")

// errNotEmpty is returned when removing a directory that still has files.
var errNotEmpty = errors.New("directory not empty")

// clientError turns an internal error into an SFTP error for the client.
// Storage details are logged but never sent to the client.
func clientError(t *target, err error) error {
	var status *sftp.StatusError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sftp.ErrSSHFxPermissionDenied),
		errors.Is(err, sftp.ErrSSHFxNoSuchFile),
		errors.Is(err, sftp.ErrSSHFxOpUnsupported),
		errors.As(err, &status):
		return err
	case gcerrors.Code(err) == gcerrors.NotFound:
		return sftp.ErrSSHFxNoSuchFile
	case errors.Is(err, errTooLarge), errors.Is(err, errNotEmpty), errors.Is(err, errSpoolFull):
		return err
	}

	storageName := ""
	if t != nil && t.folder != nil {
		storageName = t.folder.Storage
	}
	metrics.StorageErrors.WithLabelValues(storageName).Inc()
	slog.Error("storage error", "storage", storageName, "err", err)
	return sftp.ErrSSHFxFailure
}

// clientMessage is the short error text that goes into the audit log.
func clientMessage(err error) string {
	if gcerrors.Code(err) == gcerrors.NotFound {
		return "not found"
	}
	return err.Error()
}

// fileInfo describes a file or directory for SFTP listings.
type fileInfo struct {
	name    string
	size    int64
	modTime time.Time
	dir     bool
}

func (fi *fileInfo) Name() string       { return fi.name }
func (fi *fileInfo) Size() int64        { return fi.size }
func (fi *fileInfo) ModTime() time.Time { return fi.modTime }
func (fi *fileInfo) IsDir() bool        { return fi.dir }
func (fi *fileInfo) Sys() any           { return nil }

// Mode returns plain permission bits. They are only for display; the real
// rules are the folder permissions in the config.
func (fi *fileInfo) Mode() os.FileMode {
	if fi.dir {
		return os.ModeDir | 0o755
	}
	return 0o644
}

// listerAt hands a fixed list of entries to pkg/sftp page by page.
type listerAt []os.FileInfo

func (l listerAt) ListAt(dst []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(dst, l[offset:])
	if n < len(dst) {
		return n, io.EOF
	}
	return n, nil
}
