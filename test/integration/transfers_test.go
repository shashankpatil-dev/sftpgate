package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gocloud.dev/blob"
	"golang.org/x/crypto/ssh"

	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/storage"
	"github.com/shashankpatil-dev/sftpgate/internal/transfers"
)

// transferConfig is a second SFTPgate that runs transfers against the test
// server (the "partner"), with its own bucket.
const transferConfig = `
version: 1
server:
  host_keys: [HOSTKEY]
  timezone: Asia/Kolkata
storages:
  mine: mem://transfers-test
remotes:
  partner:
    addr: ADDR
    user: keyuser
    host_key: "PARTNER_HOST_KEY"
    private_key: "${file:USERKEY_FILE}"
transfers:
  - name: pull-csv
    schedule: "@every 1s"
    pull:
      from: { remote: partner, path: /data, pattern: "*.csv" }
      to: { storage: mine, key: "in/{date}/{filename}" }
  - name: pull-and-delete
    schedule: "0 0 1 1 *"
    pull:
      from: { remote: partner, path: /data, pattern: "*.del" }
      to: { storage: mine, key: "deleted/{filename}" }
      delete_after: true
  - name: push-and-move
    schedule: "0 0 1 1 *"
    push:
      from: { storage: mine, prefix: outbox/, pattern: "*.csv" }
      to: { remote: partner, path: /data }
      after: "move:sent/"
  - name: push-keep
    schedule: "0 0 1 1 *"
    push:
      from: { storage: mine, prefix: keep/ }
      to: { remote: partner, path: /data }
`

// transferEnv is a partner server plus a scheduler that talks to it.
type transferEnv struct {
	h       *harness
	sched   *transfers.Scheduler
	mine    *blob.Bucket
	partner *blob.Bucket
}

func newTransferEnv(t *testing.T, edit func(string) string) *transferEnv {
	t.Helper()
	// The partner lets keyuser change files, so pushes and deletes work.
	h := newHarness(t, func(s string) string {
		return strings.Replace(s, "prefix: key, perms: [list, upload, download] }",
			"prefix: key, perms: [list, upload, download, delete, rename, overwrite] }", 1)
	})

	hostKey, err := os.ReadFile(filepath.Join(h.dir, "host_key"))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}

	src := strings.NewReplacer(
		"HOSTKEY", filepath.Join(h.dir, "host_key"),
		"ADDR", h.addr,
		"PARTNER_HOST_KEY", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))),
		"USERKEY_FILE", filepath.Join(h.dir, "user_key"),
	).Replace(transferConfig)
	if edit != nil {
		src = edit(src)
	}
	cfg, err := config.Parse([]byte(src), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	buckets := storage.NewRegistry()
	if err := buckets.Prepare(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	sched := transfers.NewScheduler(func() *config.Config { return cfg }, buckets, t.TempDir())
	sched.Now = func() time.Time { return time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC) }

	return &transferEnv{
		h:       h,
		sched:   sched,
		mine:    buckets.Bucket(cfg, "mine"),
		partner: h.buckets.Bucket(h.cfg.Load(), "raw"),
	}
}

func put(t *testing.T, b *blob.Bucket, key, data string) {
	t.Helper()
	if err := b.WriteAll(context.Background(), key, []byte(data), nil); err != nil {
		t.Fatal(err)
	}
}

func read(b *blob.Bucket, key string) (string, bool) {
	data, err := b.ReadAll(context.Background(), key)
	return string(data), err == nil
}

func TestPullTransfer(t *testing.T) {
	e := newTransferEnv(t, nil)
	put(t, e.partner, "key/a.csv", "aaa")
	put(t, e.partner, "key/c.csv", "ccc")
	put(t, e.partner, "key/notes.txt", "not a csv")

	if err := e.sched.RunNow(context.Background(), "pull-csv"); err != nil {
		t.Fatal(err)
	}
	// 20:00 UTC is already the next day in India.
	if got, ok := read(e.mine, "in/2026-10-25/a.csv"); !ok || got != "aaa" {
		t.Fatalf("a.csv = %q, %v", got, ok)
	}
	if _, ok := read(e.mine, "in/2026-10-25/c.csv"); !ok {
		t.Fatal("c.csv was not pulled")
	}
	if _, ok := read(e.mine, "in/2026-10-25/notes.txt"); ok {
		t.Fatal("notes.txt does not match the pattern and must not be pulled")
	}
	if _, ok := read(e.partner, "key/a.csv"); !ok {
		t.Fatal("without delete_after the remote file must stay")
	}

	// A second run finds nothing new and changes nothing.
	put(t, e.mine, "in/2026-10-25/a.csv", "kept")
	if err := e.sched.RunNow(context.Background(), "pull-csv"); err != nil {
		t.Fatal(err)
	}
	if got, _ := read(e.mine, "in/2026-10-25/a.csv"); got != "kept" {
		t.Fatal("a file already in the bucket must not be pulled again")
	}

	// Every run must close its connection to the partner.
	deadline := time.Now().Add(5 * time.Second)
	for len(e.h.srv.Sessions()) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("connections to the partner were left open: %+v", e.h.srv.Sessions())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPullDeletesRemoteWhenAsked(t *testing.T) {
	e := newTransferEnv(t, nil)
	put(t, e.partner, "key/x.del", "x")

	if err := e.sched.RunNow(context.Background(), "pull-and-delete"); err != nil {
		t.Fatal(err)
	}
	if _, ok := read(e.mine, "deleted/x.del"); !ok {
		t.Fatal("file was not pulled")
	}
	if _, ok := read(e.partner, "key/x.del"); ok {
		t.Fatal("delete_after must remove the remote file")
	}
}

func TestPushTransferMovesSentFiles(t *testing.T) {
	e := newTransferEnv(t, nil)
	put(t, e.mine, "outbox/gst-oct.csv", "returns")
	put(t, e.mine, "outbox/readme.md", "not sent")

	if err := e.sched.RunNow(context.Background(), "push-and-move"); err != nil {
		t.Fatal(err)
	}
	if got, ok := read(e.partner, "key/gst-oct.csv"); !ok || got != "returns" {
		t.Fatalf("partner got %q, %v", got, ok)
	}
	if _, ok := read(e.partner, "key/.gst-oct.csv"); ok {
		t.Fatal("the temporary upload name must be renamed away")
	}
	if _, ok := read(e.mine, "outbox/gst-oct.csv"); ok {
		t.Fatal("a sent file must leave the outbox")
	}
	if _, ok := read(e.mine, "sent/gst-oct.csv"); !ok {
		t.Fatal("a sent file must be moved to sent/")
	}
	if _, ok := read(e.mine, "outbox/readme.md"); !ok {
		t.Fatal("files that don't match the pattern stay where they are")
	}
}

func TestPushKeepSkipsFilesTheRemoteHas(t *testing.T) {
	e := newTransferEnv(t, nil)
	put(t, e.mine, "keep/a.txt", "new version")
	put(t, e.partner, "key/a.txt", "partner's copy")
	put(t, e.mine, "keep/b.txt", "b")

	if err := e.sched.RunNow(context.Background(), "push-keep"); err != nil {
		t.Fatal(err)
	}
	if got, _ := read(e.partner, "key/a.txt"); got != "partner's copy" {
		t.Fatal("after: keep must not overwrite a file the remote already has")
	}
	if _, ok := read(e.partner, "key/b.txt"); !ok {
		t.Fatal("b.txt must be sent")
	}
	if _, ok := read(e.mine, "keep/b.txt"); !ok {
		t.Fatal("after: keep must leave the bucket file in place")
	}
}

func TestTransferRefusesWrongHostKey(t *testing.T) {
	// Pin some other server's key: this is what a man-in-the-middle looks like.
	other := writeKey(t, filepath.Join(t.TempDir(), "other"))
	e := newTransferEnv(t, func(s string) string {
		start := strings.Index(s, `host_key: "`) + len(`host_key: "`)
		end := start + strings.Index(s[start:], `"`)
		return s[:start] + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(other.PublicKey()))) + s[end:]
	})
	put(t, e.partner, "key/a.csv", "aaa")

	err := e.sched.RunNow(context.Background(), "pull-csv")
	if err == nil || !strings.Contains(err.Error(), "host key") {
		t.Fatalf("want a host key error, got %v", err)
	}
	if _, ok := read(e.mine, "in/2026-10-25/a.csv"); ok {
		t.Fatal("nothing may be pulled from a server with the wrong host key")
	}
}

func TestScheduleRunsTransfers(t *testing.T) {
	e := newTransferEnv(t, nil)
	put(t, e.partner, "key/scheduled.csv", "s")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.sched.Run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := read(e.mine, "in/2026-10-25/scheduled.csv"); ok {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the @every 1s schedule never ran the transfer")
}
