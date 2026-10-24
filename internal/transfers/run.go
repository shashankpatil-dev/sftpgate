package transfers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
	"golang.org/x/crypto/ssh"

	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/metrics"
	"github.com/shashankpatil-dev/sftpgate/internal/sshalgo"
	"github.com/shashankpatil-dev/sftpgate/internal/storage"
)

// dialTimeout is how long connecting and logging in to a remote may take.
const dialTimeout = 30 * time.Second

// run is one run of one transfer.
type run struct {
	s       *Scheduler
	cfg     *config.Config
	t       *config.Transfer
	ctx     context.Context
	started time.Time

	moved, skipped, failed int
}

// pull copies every matching file in the remote folder that the bucket
// doesn't have yet. One bad file doesn't stop the others.
func (r *run) pull() error {
	pl := r.t.Pull
	client, closeClient, err := r.dial(pl.From.Remote)
	if err != nil {
		return err
	}
	defer closeClient()

	entries, err := client.ReadDir(pl.From.Path)
	if err != nil {
		return fmt.Errorf("listing %s: %w", pl.From.Path, err)
	}
	bucket := r.s.Buckets.Bucket(r.cfg, pl.To.Storage)

	for _, e := range entries {
		if r.ctx.Err() != nil {
			return r.ctx.Err()
		}
		if !e.Mode().IsRegular() || !matches(pl.From.Pattern, e.Name()) {
			continue
		}

		remotePath := path.Join(pl.From.Path, e.Name())
		key := config.RenderKey(pl.To.Key, r.vars(e.Name()))
		ev := audit.Event{Event: "transfer", Transfer: r.t.Name, Path: remotePath, Storage: pl.To.Storage, Key: key, Size: e.Size()}

		if _, err := bucket.Attributes(r.ctx, key); err == nil {
			r.count("skipped")
			continue
		} else if gcerrors.Code(err) != gcerrors.NotFound {
			r.record(ev, err)
			continue
		}

		ev.SHA256, err = r.pullFile(client, remotePath, bucket, key, e.Size())
		// Another run or pod stored it in the meantime: nothing to do.
		if errors.Is(err, storage.ErrExists) {
			r.count("skipped")
			continue
		}
		if err == nil && pl.DeleteAfter {
			if err = client.Remove(remotePath); err != nil {
				err = fmt.Errorf("saved, but could not delete the remote file: %w", err)
			}
		}
		r.record(ev, err)
	}
	return r.summary()
}

// pullFile downloads one remote file to the work folder, checks its size,
// and uploads it to the bucket.
func (r *run) pullFile(client *sftp.Client, remotePath string, bucket *blob.Bucket, key string, size int64) (string, error) {
	src, err := client.Open(remotePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }()

	tmp, err := os.CreateTemp(r.s.WorkDir, "xfer-*")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	n, err := io.Copy(tmp, src)
	if err != nil {
		return "", err
	}
	// A file that is still being written on the remote may change size;
	// take it on the next run instead.
	if n != size {
		return "", fmt.Errorf("remote file changed while reading (%d bytes, expected %d)", n, size)
	}

	return storage.PutFile(r.ctx, bucket, key, tmp, n, storage.PutOptions{
		Metadata: map[string]string{
			"sftpgate-transfer": r.t.Name,
			"sftpgate-remote":   r.t.Pull.From.Remote + ":" + remotePath,
		},
		// Never replace a file that appeared since the check above.
		IfNotExist: true,
	})
}

// push sends every matching file under the storage prefix to the remote.
func (r *run) push() error {
	ps := r.t.Push
	client, closeClient, err := r.dial(ps.To.Remote)
	if err != nil {
		return err
	}
	defer closeClient()

	bucket := r.s.Buckets.Bucket(r.cfg, ps.From.Storage)
	iter := bucket.List(&blob.ListOptions{Prefix: ps.From.Prefix, Delimiter: "/"})
	for {
		obj, err := iter.Next(r.ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("listing %s: %w", ps.From.Prefix, err)
		}
		name := strings.TrimPrefix(obj.Key, ps.From.Prefix)
		if obj.IsDir || name == "" || !matches(ps.From.Pattern, name) {
			continue
		}

		remotePath := path.Join(ps.To.Path, name)
		ev := audit.Event{Event: "transfer", Transfer: r.t.Name, Path: remotePath, Storage: ps.From.Storage, Key: obj.Key, Size: obj.Size}

		if ps.After == "keep" {
			if _, err := client.Stat(remotePath); err == nil {
				r.count("skipped")
				continue
			}
		}

		err = r.pushFile(client, bucket, obj.Key, remotePath)
		if err == nil {
			err = r.afterPush(bucket, obj.Key, name)
		}
		r.record(ev, err)
	}
	return r.summary()
}

// pushFile uploads one object under a temporary name and renames it when
// complete, so the remote never sees half a file.
//
// The temporary name is the real name with a dot in front. Most servers and
// file watchers ignore dot files, and the name still ends the same way, so
// filename rules on the remote (like "*.csv") accept it.
func (r *run) pushFile(client *sftp.Client, bucket *blob.Bucket, key, remotePath string) error {
	src, err := bucket.NewReader(r.ctx, key, nil)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	tmpPath := path.Join(path.Dir(remotePath), "."+path.Base(remotePath))
	dst, err := client.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = client.Remove(tmpPath)
		return err
	}
	if err := dst.Close(); err != nil {
		_ = client.Remove(tmpPath)
		return err
	}

	// posix-rename replaces an existing file in one step. Servers without it
	// get a plain rename, after removing an old copy of the file.
	if err := client.PosixRename(tmpPath, remotePath); err == nil {
		return nil
	}
	_ = client.Remove(remotePath)
	if err := client.Rename(tmpPath, remotePath); err != nil {
		_ = client.Remove(tmpPath)
		return err
	}
	return nil
}

// afterPush deletes or moves the sent object, so the next run doesn't send it again.
func (r *run) afterPush(bucket *blob.Bucket, key, name string) error {
	ps := r.t.Push
	if prefix, ok := ps.MovePrefix(); ok {
		if err := bucket.Copy(r.ctx, prefix+name, key, nil); err != nil {
			return fmt.Errorf("sent, but could not move it to %s: %w", prefix, err)
		}
	} else if ps.After != "delete" {
		return nil
	}
	if err := bucket.Delete(r.ctx, key); err != nil {
		return fmt.Errorf("sent, but could not remove it from the bucket: %w", err)
	}
	return nil
}

// dial connects to a remote. Its host key must match the pinned one, and
// only the algorithms SFTPgate allows are offered. Call the returned
// function to close the connection.
func (r *run) dial(remoteName string) (*sftp.Client, func(), error) {
	remote := r.cfg.Remotes[remoteName]

	var auth []ssh.AuthMethod
	if remote.Signer != nil {
		auth = append(auth, ssh.PublicKeys(sshalgo.WithoutSHA1(remote.Signer)))
	}
	if remote.Password != "" {
		auth = append(auth, ssh.Password(remote.Password))
	}

	conn, err := ssh.Dial("tcp", remote.Addr, &ssh.ClientConfig{
		Config:            sshalgo.Config(),
		User:              remote.User,
		Auth:              auth,
		HostKeyCallback:   ssh.FixedHostKey(remote.Host),
		HostKeyAlgorithms: sshalgo.PublicKeys,
		Timeout:           dialTimeout,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to %s (%s): %w", remoteName, remote.Addr, err)
	}

	client, err := sftp.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("starting sftp with %s: %w", remoteName, err)
	}

	// Closing the sftp client doesn't close the SSH connection under it,
	// so both are closed, or connections to the partner would pile up.
	closeAll := func() {
		_ = client.Close()
		_ = conn.Close()
	}
	// Stop a long transfer as soon as the server shuts down.
	stop := context.AfterFunc(r.ctx, closeAll)
	return client, func() {
		stop()
		closeAll()
	}, nil
}

// record writes the audit event and metric for one file.
func (r *run) record(ev audit.Event, err error) {
	ev.Result = audit.OK
	if err != nil {
		ev.Result, ev.Error = audit.Failed, err.Error()
		r.failed++
	} else {
		r.moved++
	}
	metrics.TransferFiles.WithLabelValues(r.t.Name, ev.Result).Inc()
	audit.Emit(ev)
}

func (r *run) count(result string) {
	r.skipped++
	metrics.TransferFiles.WithLabelValues(r.t.Name, result).Inc()
}

// summary fails the run if any file failed, so alerts notice.
func (r *run) summary() error {
	if r.failed > 0 {
		return fmt.Errorf("%d of %d files failed; see the transfer events", r.failed, r.failed+r.moved)
	}
	return nil
}

// vars returns the values for {placeholders} in pull keys.
func (r *run) vars(filename string) map[string]string {
	t := r.started.In(r.cfg.Server.Location)
	return map[string]string{
		"name":     r.t.Name,
		"date":     t.Format("2006-01-02"),
		"time":     t.Format("150405"),
		"filename": filename,
	}
}

// matches reports whether name fits the pattern. No pattern matches all.
func matches(pattern, name string) bool {
	if pattern == "" {
		return true
	}
	ok, _ := path.Match(pattern, name)
	return ok
}
