package vfs

import (
	"context"
	"path"

	"github.com/pkg/sftp"
	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	"github.com/shashankpatil-dev/sftpgate/internal/config"
)

// Filecmd handles the requests that change files without moving data:
// remove, rename, mkdir, rmdir and setstat.
func (h *Handler) Filecmd(r *sftp.Request) error {
	switch r.Method {
	case "Setstat":
		// Clients like WinSCP set the file time after an upload. Buckets have
		// no such setting, so we say yes and do nothing.
		return nil
	case "Remove":
		return h.remove(r)
	case "Mkdir":
		return h.mkdir(r)
	case "Rmdir":
		return h.rmdir(r)
	case "Rename":
		return h.rename(r)
	default:
		return sftp.ErrSSHFxOpUnsupported
	}
}

func (h *Handler) remove(r *sftp.Request) error {
	t, err := h.resolve(r.Filepath)
	if err != nil {
		return err
	}
	e := h.event("delete", t)

	if !t.allowed(config.PermDelete) {
		return h.finish(e, t, sftp.ErrSSHFxPermissionDenied)
	}
	return h.finish(e, t, t.bucket.Delete(r.Context(), t.key))
}

// mkdir creates a directory. Buckets have no real directories, so we store
// an empty object whose name ends in "/", like most bucket tools do.
func (h *Handler) mkdir(r *sftp.Request) error {
	t, err := h.resolve(r.Filepath)
	if err != nil {
		return err
	}
	e := h.event("mkdir", t)

	if !t.allowed(config.PermMkdir) {
		return h.finish(e, t, sftp.ErrSSHFxPermissionDenied)
	}
	return h.finish(e, t, t.bucket.WriteAll(r.Context(), t.key+"/", nil, nil))
}

// rmdir removes an empty directory.
func (h *Handler) rmdir(r *sftp.Request) error {
	t, err := h.resolve(r.Filepath)
	if err != nil {
		return err
	}
	e := h.event("rmdir", t)

	if !t.allowed(config.PermDelete) {
		return h.finish(e, t, sftp.ErrSSHFxPermissionDenied)
	}

	marker := t.key + "/"
	page, _, err := t.bucket.ListPage(r.Context(), blob.FirstPageToken, 2, &blob.ListOptions{Prefix: marker})
	if err != nil {
		return h.finish(e, t, err)
	}
	if len(page) == 0 {
		return h.finish(e, t, sftp.ErrSSHFxNoSuchFile)
	}
	// Anything in the listing other than the directory marker means it isn't empty.
	for _, obj := range page {
		if obj.Key != marker {
			return h.finish(e, t, errNotEmpty)
		}
	}
	return h.finish(e, t, t.bucket.Delete(r.Context(), marker))
}

// rename moves a file inside one folder. Buckets can't rename, so it is a
// copy followed by a delete. If the copy fails, nothing has changed.
func (h *Handler) rename(r *sftp.Request) error {
	src, err := h.resolve(r.Filepath)
	if err != nil {
		return err
	}
	e := h.event("rename", src)

	dst, err := h.resolve(r.Target)
	if err != nil {
		return h.finish(e, src, err)
	}
	e.ToPath, e.ToKey = dst.path, dst.key

	// Moving between folders could cross buckets and permission rules, so
	// it isn't allowed.
	if !src.allowed(config.PermRename) || dst.folder != src.folder || dst.rel == "" {
		return h.finish(e, src, sftp.ErrSSHFxPermissionDenied)
	}
	if !matchesPatterns(dst.folder, path.Base(dst.rel)) {
		return h.finish(e, src, sftp.ErrSSHFxPermissionDenied)
	}

	ctx := r.Context()
	if ok, err := exists(ctx, src); err != nil || !ok {
		return h.finish(e, src, notFoundIfNil(err))
	}
	if ok, err := exists(ctx, dst); err != nil {
		return h.finish(e, src, err)
	} else if ok && !src.folder.Perm.Has(config.PermOverwrite) {
		return h.finish(e, src, sftp.ErrSSHFxPermissionDenied)
	}

	if err := src.bucket.Copy(ctx, dst.key, src.key, nil); err != nil {
		return h.finish(e, src, err)
	}
	return h.finish(e, src, src.bucket.Delete(ctx, src.key))
}

func notFoundIfNil(err error) error {
	if err == nil {
		return sftp.ErrSSHFxNoSuchFile
	}
	return err
}

// isDir reports whether anything is stored under the target as a directory.
func isDir(ctx context.Context, t *target) (bool, error) {
	page, _, err := t.bucket.ListPage(ctx, blob.FirstPageToken, 1, &blob.ListOptions{Prefix: t.key + "/"})
	if gcerrors.Code(err) == gcerrors.NotFound {
		return false, nil
	}
	return len(page) > 0, err
}
