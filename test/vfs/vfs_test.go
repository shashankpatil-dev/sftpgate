package vfs_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"gocloud.dev/blob"

	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/storage"
	"github.com/shashankpatil-dev/sftpgate/internal/vfs"
)

const testConfig = `
version: 1
server:
  host_keys: [unused-offline]
  spool_dir: SPOOL
  min_spool_free: 1MiB
storages:
  raw: mem://
accounts:
  - name: acme
    auth:
      public_keys: ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"]
    folders:
      - path: /inbound
        storage: raw
        prefix: acme/in
        perms: [upload]
        patterns: ["*.csv"]
        max_file_size: 1KiB
      - path: /outbound
        storage: raw
        prefix: acme/out
        perms: [list, download, upload, overwrite, delete, rename, mkdir]
`

// env is one SFTP client talking to a vfs.Handler over an in-memory pipe.
type env struct {
	client *sftp.Client
	bucket *blob.Bucket

	// done is closed when the server side has finished.
	done chan struct{}

	// clientConn closes the client side, to simulate a dropped connection.
	clientConn io.Closer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	audit.SetOutput(io.Discard)

	src := strings.Replace(testConfig, "SPOOL", t.TempDir(), 1)
	cfg, err := config.Parse([]byte(src), config.Options{Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	reg := storage.NewRegistry()
	if err := reg.Prepare(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	h := &vfs.Handler{Config: cfg, Account: cfg.Account("acme"), Buckets: reg, SessionID: "s1", IP: "10.0.0.1"}

	serverR, clientW := io.Pipe()
	clientR, serverW := io.Pipe()
	srv := sftp.NewRequestServer(struct {
		io.Reader
		io.WriteCloser
	}{serverR, serverW}, h.Handlers())

	e := &env{bucket: reg.Bucket(cfg, "raw"), done: make(chan struct{}), clientConn: clientW}
	go func() {
		_ = srv.Serve()
		_ = srv.Close()
		close(e.done)
	}()

	e.client, err = sftp.NewClientPipe(clientR, clientW)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.client.Close() })
	return e
}

func (e *env) put(p, data string) error {
	f, err := e.client.Create(p)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(data)); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (e *env) object(t *testing.T, key string) (string, bool) {
	t.Helper()
	b, err := e.bucket.ReadAll(context.Background(), key)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func isPermissionDenied(err error) bool {
	return errors.Is(err, os.ErrPermission)
}

// isRejected reports whether the server refused the request, either as
// "permission denied" or as "no such file".
func isRejected(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist)
}

func TestUploadLandsUnderPrefixWithChecksum(t *testing.T) {
	e := newEnv(t)
	if err := e.put("/inbound/day1.csv", "a,b,c\n"); err != nil {
		t.Fatal(err)
	}

	got, ok := e.object(t, "acme/in/day1.csv")
	if !ok || got != "a,b,c\n" {
		t.Fatalf("object = %q, %v", got, ok)
	}
	attrs, _ := e.bucket.Attributes(context.Background(), "acme/in/day1.csv")
	sum := sha256.Sum256([]byte("a,b,c\n"))
	if attrs.Metadata["sftpgate-sha256"] != hex.EncodeToString(sum[:]) || attrs.Metadata["sftpgate-account"] != "acme" {
		t.Fatalf("metadata = %v", attrs.Metadata)
	}
}

func TestUploadRules(t *testing.T) {
	e := newEnv(t)
	if err := e.put("/inbound/day1.csv", "x"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, path, data string
	}{
		{"pattern blocks other file types", "/inbound/virus.exe", "x"},
		{"no overwrite permission", "/inbound/day1.csv", "y"},
		{"root is read-only", "/top.csv", "x"},
		{"dotdot cannot escape to root", "/inbound/../../top.csv", "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := e.put(tc.path, tc.data); !isRejected(err) {
				t.Fatalf("want the upload rejected, got %v", err)
			}
		})
	}

	if got, _ := e.object(t, "acme/in/day1.csv"); got != "x" {
		t.Fatalf("original file changed to %q", got)
	}
}

func TestTooLargeUploadIsNotSaved(t *testing.T) {
	e := newEnv(t)
	err := e.put("/inbound/big.csv", strings.Repeat("x", 2048))
	if err == nil {
		t.Fatal("want error for file over max_file_size")
	}
	if _, ok := e.object(t, "acme/in/big.csv"); ok {
		t.Fatal("too-large file must not reach the bucket")
	}
}

func TestDroppedConnectionIsNotSaved(t *testing.T) {
	e := newEnv(t)
	f, err := e.client.Create("/inbound/half.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("half of the")); err != nil {
		t.Fatal(err)
	}

	// Cut the connection without closing the file, like a crashed client.
	_ = e.clientConn.Close()
	select {
	case <-e.done:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not notice the dropped connection")
	}

	if _, ok := e.object(t, "acme/in/half.csv"); ok {
		t.Fatal("half-uploaded file must not reach the bucket")
	}
}

func TestUploadOnlyFolderHidesFiles(t *testing.T) {
	e := newEnv(t)
	if err := e.put("/inbound/day1.csv", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.Open("/inbound/day1.csv"); !isPermissionDenied(err) {
		t.Fatalf("download: want permission denied, got %v", err)
	}
	if _, err := e.client.ReadDir("/inbound"); !isPermissionDenied(err) {
		t.Fatalf("list: want permission denied, got %v", err)
	}
}

func TestDownloadAndList(t *testing.T) {
	e := newEnv(t)
	data := strings.Repeat("0123456789", 10_000)
	if err := e.bucket.WriteAll(context.Background(), "acme/out/report.csv", []byte(data), nil); err != nil {
		t.Fatal(err)
	}

	f, err := e.client.Open("/outbound/report.csv")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if buf.String() != data {
		t.Fatalf("downloaded %d bytes, want %d", buf.Len(), len(data))
	}

	root, err := e.client.ReadDir("/")
	if err != nil || len(root) != 2 || root[0].Name() != "inbound" || !root[0].IsDir() {
		t.Fatalf("root listing = %v, %v", names(root), err)
	}
	out, err := e.client.ReadDir("/outbound")
	if err != nil || len(out) != 1 || out[0].Name() != "report.csv" || out[0].Size() != int64(len(data)) {
		t.Fatalf("outbound listing = %v, %v", names(out), err)
	}
}

func TestDirectoriesAndRename(t *testing.T) {
	e := newEnv(t)
	c := e.client

	if err := c.Mkdir("/outbound/2026"); err != nil {
		t.Fatal(err)
	}
	if err := e.put("/outbound/2026/a.tmp", "data"); err != nil {
		t.Fatal(err)
	}
	if fi, err := c.Stat("/outbound/2026"); err != nil || !fi.IsDir() {
		t.Fatalf("stat dir = %v, %v", fi, err)
	}
	if err := c.RemoveDirectory("/outbound/2026"); err == nil {
		t.Fatal("removing a non-empty directory must fail")
	}

	if err := c.Rename("/outbound/2026/a.tmp", "/outbound/2026/a.csv"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.object(t, "acme/out/2026/a.csv"); !ok {
		t.Fatal("renamed file missing")
	}
	if _, ok := e.object(t, "acme/out/2026/a.tmp"); ok {
		t.Fatal("old name still there after rename")
	}
	if err := c.Rename("/outbound/2026/a.csv", "/inbound/a.csv"); !isPermissionDenied(err) {
		t.Fatalf("rename across folders: want permission denied, got %v", err)
	}

	// WinSCP and FileZilla set the file time after upload; it must not fail.
	if err := c.Chtimes("/outbound/2026/a.csv", time.Now(), time.Now()); err != nil {
		t.Fatalf("setstat: %v", err)
	}

	if err := c.Remove("/outbound/2026/a.csv"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveDirectory("/outbound/2026"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stat("/outbound/2026"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory still there: %v", err)
	}
}

func names(fis []os.FileInfo) []string {
	var out []string
	for _, fi := range fis {
		out = append(out, fi.Name())
	}
	return out
}

func TestFreeSpace(t *testing.T) {
	free, err := vfs.FreeSpace(t.TempDir())
	if err != nil || free <= 0 {
		t.Fatalf("free space = %d, %v", free, err)
	}
}
