package vfs

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	"github.com/shashankpatil-dev/sftpgate/internal/config"
)

// maxListEntries caps how many entries one directory listing returns.
//
// shortcut: the listing is built in memory, so it is capped; switch to a
// streaming lister if partners keep more files than this in one directory.
const maxListEntries = 10_000

var errTooManyEntries = errors.New("directory has too many entries to list")

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

	var out listerAt
	iter := t.bucket.List(&blob.ListOptions{Prefix: prefix, Delimiter: "/"})
	for {
		obj, err := iter.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, clientError(t, err)
		}
		// Skip the directory's own marker object.
		if obj.Key == prefix {
			continue
		}
		if len(out) == maxListEntries {
			return nil, errTooManyEntries
		}
		name := strings.TrimSuffix(strings.TrimPrefix(obj.Key, prefix), "/")
		out = append(out, &fileInfo{name: name, size: obj.Size, modTime: obj.ModTime, dir: obj.IsDir})
	}

	h.count("list", t, "ok")
	return out, nil
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
