package vfs

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	"github.com/shashankpatil-dev/sftpgate/internal/config"
)

var errOutOfOrder = errors.New("directory entries must be read in order")

// Filelist handles directory listings and stat requests.
func (h *Handler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	t, err := h.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}

	switch r.Method {
	case "List":
		return h.list(r.Context(), t)
	case "Stat", "Lstat":
		fi, err := h.stat(r.Context(), t)
		if err != nil {
			return nil, err
		}
		return listerAt{fi}, nil
	default:
		return nil, sftp.ErrSSHFxOpUnsupported
	}
}

func (h *Handler) list(ctx context.Context, t *target) (sftp.ListerAt, error) {
	// The root lists the account's folders. Any account may see its own folders.
	if t.folder == nil {
		var out listerAt
		for _, f := range h.Account.Folders {
			out = append(out, &fileInfo{name: f.Path[1:], dir: true})
		}
		return out, nil
	}

	if !t.folder.Perm.Has(config.PermList) {
		return nil, h.deny("list", t)
	}

	prefix := t.folder.Prefix
	if t.rel != "" {
		prefix = t.key + "/"
		ok, err := isDir(ctx, t)
		if err != nil {
			return nil, clientError(t, err)
		}
		if !ok {
			return nil, sftp.ErrSSHFxNoSuchFile
		}
	}

	h.count("list", t, "ok")
	// The listing outlives this request, so it can't use the request's context.
	return &streamLister{
		t:      t,
		prefix: prefix,
		iter:   t.bucket.List(&blob.ListOptions{Prefix: prefix, Delimiter: "/"}),
	}, nil
}

// streamLister hands directory entries to pkg/sftp page by page, reading
// the bucket only as far as the client asks. A folder of any size is
// listed without holding it all in memory.
type streamLister struct {
	t      *target
	prefix string
	iter   *blob.ListIterator

	mu sync.Mutex
	// pos is how many entries were handed out so far.
	pos int64
}

func (l *streamLister) ListAt(dst []os.FileInfo, offset int64) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// SFTP clients read a directory from start to end, so only the next
	// page is ever asked for.
	if offset != l.pos {
		return 0, errOutOfOrder
	}

	n := 0
	for n < len(dst) {
		obj, err := l.iter.Next(context.Background())
		if errors.Is(err, io.EOF) {
			l.pos += int64(n)
			return n, io.EOF
		}
		if err != nil {
			return n, clientError(l.t, err)
		}
		// Skip the directory's own marker object.
		if obj.Key == l.prefix {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(obj.Key, l.prefix), "/")
		dst[n] = &fileInfo{name: name, size: obj.Size, modTime: obj.ModTime, dir: obj.IsDir}
		n++
	}
	l.pos += int64(n)
	return n, nil
}

// stat describes one path.
//
// Any permission on the folder allows stat, because clients check if a file
// exists before uploading or downloading it.
func (h *Handler) stat(ctx context.Context, t *target) (os.FileInfo, error) {
	if t.folder == nil {
		return &fileInfo{name: "/", dir: true}, nil
	}
	if t.rel == "" {
		return &fileInfo{name: t.folder.Path[1:], dir: true}, nil
	}

	attrs, err := t.bucket.Attributes(ctx, t.key)
	if err == nil {
		return &fileInfo{name: path.Base(t.rel), size: attrs.Size, modTime: attrs.ModTime}, nil
	}
	if gcerrors.Code(err) != gcerrors.NotFound {
		return nil, clientError(t, err)
	}

	ok, err := isDir(ctx, t)
	if err != nil {
		return nil, clientError(t, err)
	}
	if !ok {
		return nil, sftp.ErrSSHFxNoSuchFile
	}
	return &fileInfo{name: path.Base(t.rel), dir: true}, nil
}

// deny records a refused request and returns the permission error.
func (h *Handler) deny(op string, t *target) error {
	return h.finish(h.event(op, t), t, sftp.ErrSSHFxPermissionDenied)
}
