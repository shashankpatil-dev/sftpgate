package transfers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"go.opentelemetry.io/otel/trace"
	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
	"golang.org/x/crypto/ssh"

	"github.com/shashankpatil-dev/sftpgate/internal/actions"
	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/metrics"
	"github.com/shashankpatil-dev/sftpgate/internal/sshalgo"
	"github.com/shashankpatil-dev/sftpgate/internal/storage"
	"github.com/shashankpatil-dev/sftpgate/internal/tracing"
	"github.com/shashankpatil-dev/sftpgate/internal/vfs"
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
			r.record(r.ctx, ev, err)
			continue
		}

		ctx, span := r.fileSpan(pl.To.Storage, e.Size())
		ev.SHA256, err = r.pullFile(ctx, client, remotePath, bucket, key, e.Name(), e.Size())
		// Another run or pod stored it in the meantime: nothing to do.
		if errors.Is(err, storage.ErrExists) {
			r.count("skipped")
			tracing.Finish(span, "skipped", nil)
			continue
		}
		if err == nil {
			r.enqueue(ctx, config.AfterPull, vfs.FileEvent{
				Storage: pl.To.Storage, Key: key, Filename: e.Name(), Size: e.Size(), SHA256: ev.SHA256, RemotePath: remotePath,
			})
		}
		if err == nil && pl.DeleteAfter {
			if err = client.Remove(remotePath); err != nil {
				err = fmt.Errorf("saved, but could not delete the remote file: %w", err)
			}
		}
		span.SetAttributes(tracing.SHA256.String(ev.SHA256))
		r.record(ctx, ev, err)
		r.finishFile(span, err)
	}
	return r.summary()
}

// pullFile downloads one remote file to the work folder, checks its size,
// runs the before_pull checks, and uploads it to the bucket.
func (r *run) pullFile(ctx context.Context, client *sftp.Client, remotePath string, bucket *blob.Bucket, key, name string, size int64) (string, error) {
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
	if steps := r.t.Hooks.Steps(config.BeforePull); len(steps) > 0 {
		_, _, cleanup, err := r.before(ctx, steps, tmp.Name(), name)
		if cleanup != nil {
			cleanup()
		}
		if err != nil {
			return "", err
		}
	}

	return storage.PutFile(ctx, bucket, key, tmp, n, storage.PutOptions{
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

		steps := r.t.Hooks.Steps(config.BeforePush)
		remotePath := path.Join(ps.To.Path, actions.OutName(steps, name))
		ev := audit.Event{Event: "transfer", Transfer: r.t.Name, Path: remotePath, Storage: ps.From.Storage, Key: obj.Key, Size: obj.Size}

		if ps.After == "keep" {
			if _, err := client.Stat(remotePath); err == nil {
				r.count("skipped")
				continue
			}
		}

		ctx, span := r.fileSpan(ps.From.Storage, obj.Size)
		err = r.send(ctx, client, bucket, obj.Key, name, remotePath, steps)
		finalKey := obj.Key
		if err == nil {
			finalKey, err = r.afterPush(bucket, obj.Key, name)
		}
		if err == nil {
			r.enqueue(ctx, config.AfterPush, vfs.FileEvent{
				Storage: ps.From.Storage, Key: finalKey, Filename: name, Size: obj.Size, RemotePath: remotePath,
			})
		}
		r.record(ctx, ev, err)
		r.finishFile(span, err)
	}
	return r.summary()
}

// send pushes one object to the remote. With before_push actions, a local
// copy is checked (and maybe encrypted) first, and that is what gets sent.
// A file that fails a check isn't sent, and stays where it is.
func (r *run) send(ctx context.Context, client *sftp.Client, bucket *blob.Bucket, key, name, remotePath string, steps []*config.Step) error {
	src, err := bucket.NewReader(ctx, key, nil)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	if len(steps) == 0 {
		return r.pushFile(client, src, remotePath)
	}

	tmp, err := os.CreateTemp(r.s.WorkDir, "xfer-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = io.Copy(tmp, src)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}

	outPath, _, cleanup, err := r.before(ctx, steps, tmp.Name(), name)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return err
	}
	out, err := os.Open(outPath)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	return r.pushFile(client, out, remotePath)
}

// before runs a before_* hook on a local copy of a file.
func (r *run) before(ctx context.Context, steps []*config.Step, path, name string) (string, string, func(), error) {
	if r.s.Before == nil {
		return "", "", nil, errors.New("before hooks can't run")
	}
	return r.s.Before(ctx, steps, path, name)
}

// fileSpan starts the transfer.file span of one file, inside the run's trace.
func (r *run) fileSpan(storageName string, size int64) (context.Context, trace.Span) {
	return tracing.Tracer.Start(r.ctx, "transfer.file", trace.WithAttributes(
		tracing.Transfer.String(r.t.Name), tracing.Storage.String(storageName), tracing.Size.Int64(size)))
}

// finishFile ends a file's span. A file refused by a before hook is
// "denied", like a refused upload.
func (r *run) finishFile(span trace.Span, err error) {
	var rejected *actions.Rejected
	if errors.As(err, &rejected) {
		tracing.Finish(span, audit.Denied, err)
		return
	}
	tracing.End(span, err)
}

// enqueue queues a file's after_* actions, if the transfer has any. The
// file has already moved, so a failure is logged, not counted against it.
func (r *run) enqueue(ctx context.Context, hook string, ev vfs.FileEvent) {
	if len(r.t.Hooks.Steps(hook)) == 0 || r.s.Enqueue == nil {
		return
	}
	ev.Hook, ev.Transfer, ev.Time = hook, r.t.Name, r.s.Now()
	ev.TraceParent = tracing.Inject(ctx)
	if r.t.Pull != nil {
		ev.Remote = r.t.Pull.From.Remote
	} else {
		ev.Remote = r.t.Push.To.Remote
	}
	if err := r.s.Enqueue(ev); err != nil {
		slog.Error("could not queue actions", "hook", hook, "transfer", r.t.Name, "key", ev.Key, "err", err)
		metrics.Actions.WithLabelValues("queue", "failed").Inc()
	}
}

// pushFile uploads one object under a temporary name and renames it when
// complete, so the remote never sees half a file.
//
// The temporary name is the real name with a dot in front. Most servers and
// file watchers ignore dot files, and the name still ends the same way, so
// filename rules on the remote (like "*.csv") accept it.
func (r *run) pushFile(client *sftp.Client, src io.Reader, remotePath string) error {
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

// afterPush deletes or moves the sent object, so the next run doesn't send
// it again. It returns where the object is now.
func (r *run) afterPush(bucket *blob.Bucket, key, name string) (string, error) {
	ps := r.t.Push
	moved := key
	if prefix, ok := ps.MovePrefix(); ok {
		moved = prefix + name
		if err := bucket.Copy(r.ctx, moved, key, nil); err != nil {
			return key, fmt.Errorf("sent, but could not move it to %s: %w", prefix, err)
		}
	} else if ps.After != "delete" {
		return key, nil
	}
	if err := bucket.Delete(r.ctx, key); err != nil {
		return key, fmt.Errorf("sent, but could not remove it from the bucket: %w", err)
	}
	return moved, nil
}

// dial connects to a remote. Its host key must match the pinned one, and
// only the algorithms SFTPgate allows are offered. Call the returned
// function to close the connection.
func (r *run) dial(remoteName string) (_ *sftp.Client, _ func(), err error) {
	remote := r.cfg.Remotes[remoteName]
	_, span := tracing.Tracer.Start(r.ctx, "transfer.connect", trace.WithAttributes(tracing.Remote.String(remoteName)))
	defer func() { tracing.End(span, err) }()

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
func (r *run) record(ctx context.Context, ev audit.Event, err error) {
	ev.Result = audit.OK
	ev.TraceID = tracing.TraceID(ctx)
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
