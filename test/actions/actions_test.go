package actions_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"gocloud.dev/blob"
	"golang.org/x/crypto/ssh"

	"github.com/shashankpatil-dev/sftpgate/internal/actions"
	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/storage"
	"github.com/shashankpatil-dev/sftpgate/internal/vfs"
)

const (
	passphrase = "pgp test passphrase"
	hookSecret = "hook test secret"
)

// configTemplate defines named actions and folders that run them from their
// hooks. The last folder uses the older on_upload form, which must still work.
const configTemplate = `
version: 1
server:
  host_keys: [HOST_KEY_FILE]
  timezone: Asia/Kolkata
  queue: { max_attempts: 3, backoff: 1m }
storages:
  raw: mem://raw
  out: mem://out
actions:
  decrypt:
    pgp_decrypt: { key: "${file:PRIVATE_KEY_FILE}", passphrase: "${env:PGP_PASS}" }
  decrypt-signed:
    pgp_decrypt:
      key: "${file:PRIVATE_KEY_FILE}"
      passphrase: "${env:PGP_PASS}"
      signer_keys: ["${file:SIGNER_KEY_FILE}"]
  encrypt:
    pgp_encrypt: { recipients: ["${file:PUBLIC_KEY_FILE}"] }
  plain-copy:
    copy: { storage: out, key: "plain/{account}/{date}/{filename}" }
  enc-copy:
    copy: { storage: out, key: "encrypted/{folder}/{filename}" }
  verified-copy:
    copy: { storage: out, key: "verified/{filename}" }
  recon-csv:
    check: { csv: { header: [id, amount], min_rows: 1 } }
  notify:
    webhook: { url: "HOOK_URL", secret: "${env:HOOK_SECRET}", headers: { X-Team: recon } }
accounts:
  - name: acme
    auth: { public_keys: ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"] }
    folders:
      - path: /in
        storage: raw
        prefix: in/
        perms: [upload]
        hooks: { after_upload: [decrypt, plain-copy, notify] }
      - path: /enc
        storage: raw
        prefix: enc/
        perms: [upload]
        hooks: { after_upload: [encrypt, enc-copy] }
      - path: /signed
        storage: raw
        prefix: signed/
        perms: [upload]
        hooks: { after_upload: [decrypt-signed, verified-copy] }
      - path: /checked
        storage: raw
        prefix: checked/
        perms: [upload]
        hooks: { after_upload: [decrypt, recon-csv, plain-copy] }
      - path: /out
        storage: raw
        prefix: out/
        perms: [download]
        hooks: { after_download: [notify] }
      - path: /hook
        storage: raw
        prefix: hook/
        perms: [upload]
        on_upload:
          - webhook: { url: "HOOK_URL" }
`

// env is one runner with in-memory buckets and a fake webhook receiver.
type env struct {
	t      *testing.T
	runner *actions.Runner
	cfg    atomic.Pointer[config.Config]
	raw    *blob.Bucket
	out    *blob.Bucket
	hook   *hookServer
	entity *openpgp.Entity
	signer *openpgp.Entity
	now    time.Time
	dir    string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	audit.SetOutput(io.Discard)
	e := &env{t: t, dir: t.TempDir(), hook: newHookServer(t)}
	e.now = time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC)

	e.entity = writeKeys(t, e.dir)
	e.signer = newPGPEntity(t)
	writeFile(t, filepath.Join(e.dir, "signer.asc"), armored(t, openpgp.PublicKeyType, e.signer.Serialize))
	t.Setenv("PGP_PASS", passphrase)
	t.Setenv("HOOK_SECRET", hookSecret)

	src := strings.NewReplacer(
		"HOST_KEY_FILE", filepath.Join(e.dir, "host_key"),
		"PRIVATE_KEY_FILE", filepath.Join(e.dir, "private.asc"),
		"PUBLIC_KEY_FILE", filepath.Join(e.dir, "public.asc"),
		"SIGNER_KEY_FILE", filepath.Join(e.dir, "signer.asc"),
		"HOOK_URL", e.hook.URL,
	).Replace(configTemplate)
	e.load(src)

	buckets := storage.NewRegistry()
	if err := buckets.Prepare(context.Background(), e.cfg.Load()); err != nil {
		t.Fatal(err)
	}
	e.raw = buckets.Bucket(e.cfg.Load(), "raw")
	e.out = buckets.Bucket(e.cfg.Load(), "out")

	r, err := actions.NewRunner(filepath.Join(e.dir, "queue"), e.dir, e.cfg.Load, buckets)
	if err != nil {
		t.Fatal(err)
	}
	r.Now = func() time.Time { return e.now }
	e.runner = r
	return e
}

func (e *env) load(src string) {
	e.t.Helper()
	cfg, err := config.Parse([]byte(src), config.Options{})
	if err != nil {
		e.t.Fatal(err)
	}
	e.cfg.Store(cfg)
}

// upload stores a file in the raw bucket and queues its steps, like the
// SFTP server does after a successful upload.
func (e *env) upload(folder, filename string, data []byte) {
	e.t.Helper()
	key := strings.TrimPrefix(folder, "/") + "/" + filename
	sha, err := storage.PutFile(context.Background(), e.raw, key, bytes.NewReader(data), int64(len(data)), storage.PutOptions{})
	if err != nil {
		e.t.Fatal(err)
	}
	err = e.runner.Add(vfs.FileEvent{
		Hook: config.AfterUpload, Account: "acme", Folder: folder, Storage: "raw", Key: key,
		Filename: filename, Size: int64(len(data)), SHA256: sha, Time: e.now,
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) read(b *blob.Bucket, key string) (string, bool) {
	data, err := b.ReadAll(context.Background(), key)
	return string(data), err == nil
}

func (e *env) queued(sub string) []string {
	files, _ := filepath.Glob(filepath.Join(e.dir, "queue", sub, "*.json"))
	return files
}

func TestDecryptCopyAndWebhook(t *testing.T) {
	e := newEnv(t)
	e.upload("/in", "settlement.csv.pgp", encrypt(t, e.entity, "id,amount\n1,100\n"))
	e.runner.RunDue(context.Background())

	// 20:00 UTC is already the next day in India, so {date} is 2026-10-25.
	got, ok := e.read(e.out, "plain/acme/2026-10-25/settlement.csv")
	if !ok || got != "id,amount\n1,100\n" {
		t.Fatalf("decrypted copy = %q, %v", got, ok)
	}
	if _, ok := e.read(e.raw, "in/settlement.csv.pgp"); !ok {
		t.Fatal("the uploaded file must stay as it was")
	}

	calls := e.hook.calls()
	if len(calls) != 1 {
		t.Fatalf("webhook calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.signature != "sha256="+actions.Sign(hookSecret, call.body) {
		t.Fatalf("bad signature %q", call.signature)
	}
	var p actions.WebhookPayload
	if err := json.Unmarshal(call.body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Key != "in/settlement.csv.pgp" || len(p.Copies) != 1 || p.Copies[0].Key != "plain/acme/2026-10-25/settlement.csv" {
		t.Fatalf("webhook payload = %+v", p)
	}
	if len(e.queued("")) != 0 {
		t.Fatal("a finished job must be removed from the queue")
	}
}

func TestSignatureIsVerified(t *testing.T) {
	e := newEnv(t)
	stranger := newPGPEntity(t)

	e.upload("/signed", "good.csv.pgp", encryptSigned(t, e.entity, e.signer, "signed by the partner"))
	e.upload("/signed", "unsigned.csv.pgp", encrypt(t, e.entity, "not signed"))
	e.upload("/signed", "stranger.csv.pgp", encryptSigned(t, e.entity, stranger, "signed by someone else"))
	e.runner.RunDue(context.Background())

	if got, ok := e.read(e.out, "verified/good.csv"); !ok || got != "signed by the partner" {
		t.Fatalf("a correctly signed file must pass, got %q, %v", got, ok)
	}
	for _, name := range []string{"unsigned.csv", "stranger.csv"} {
		if _, ok := e.read(e.out, "verified/"+name); ok {
			t.Errorf("%s must not be passed on", name)
		}
	}

	var errs []string
	for _, f := range e.queued("failed") {
		var job actions.Job
		b, _ := os.ReadFile(f)
		_ = json.Unmarshal(b, &job)
		errs = append(errs, job.LastError)
	}
	all := strings.Join(errs, " | ")
	if len(errs) != 2 || !strings.Contains(all, "not signed") || !strings.Contains(all, "unknown key") {
		t.Fatalf("want one unsigned and one unknown-key failure, got %s", all)
	}
}

func TestEncryptCopy(t *testing.T) {
	e := newEnv(t)
	e.upload("/enc", "report.csv", []byte("secret numbers"))
	e.runner.RunDue(context.Background())

	enc, ok := e.read(e.out, "encrypted/enc/report.csv.pgp")
	if !ok {
		t.Fatal("encrypted copy missing")
	}
	md, err := openpgp.ReadMessage(strings.NewReader(enc), openpgp.EntityList{e.entity}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(md.UnverifiedBody)
	if string(plain) != "secret numbers" {
		t.Fatalf("decrypted back to %q", plain)
	}
}

func TestRetryWithBackoffThenSuccess(t *testing.T) {
	e := newEnv(t)
	e.hook.fail(2)
	e.upload("/hook", "a.csv", []byte("x"))

	// Attempt 1 fails: retry after 1m.
	e.runner.RunDue(context.Background())
	job := onlyJob(t, e.queued(""))
	if job.Attempt != 1 || !job.NextAt.Equal(e.now.Add(time.Minute)) || !strings.Contains(job.LastError, "500") {
		t.Fatalf("after first failure: %+v", job)
	}

	// Not due yet: nothing runs.
	e.runner.RunDue(context.Background())
	if len(e.hook.calls()) != 1 {
		t.Fatal("a job must wait for its retry time")
	}

	// Attempt 2 fails: the wait doubles to 2m.
	e.now = e.now.Add(time.Minute)
	e.runner.RunDue(context.Background())
	job = onlyJob(t, e.queued(""))
	if job.Attempt != 2 || !job.NextAt.Equal(e.now.Add(2*time.Minute)) {
		t.Fatalf("after second failure: %+v", job)
	}

	// Attempt 3 works.
	e.now = e.now.Add(2 * time.Minute)
	e.runner.RunDue(context.Background())
	if len(e.queued("")) != 0 || len(e.queued("failed")) != 0 || len(e.hook.calls()) != 3 {
		t.Fatalf("job should be done after the third call (calls %d)", len(e.hook.calls()))
	}
}

func TestGiveUpAfterMaxAttempts(t *testing.T) {
	e := newEnv(t)
	e.hook.fail(100)
	e.upload("/hook", "a.csv", []byte("x"))

	for range 3 {
		e.runner.RunDue(context.Background())
		e.now = e.now.Add(time.Hour)
	}
	if len(e.queued("")) != 0 || len(e.queued("failed")) != 1 {
		t.Fatal("after max_attempts the job must move to failed")
	}
	if job := onlyJob(t, e.queued("failed")); job.Attempt != 3 {
		t.Fatalf("failed job attempts = %d, want 3", job.Attempt)
	}
}

// Retry puts failed jobs back with their attempts reset, and they run again.
func TestRetryFailedJobs(t *testing.T) {
	e := newEnv(t)
	e.hook.fail(100)
	e.upload("/hook", "a.csv", []byte("x"))
	for range 3 {
		e.runner.RunDue(context.Background())
		e.now = e.now.Add(time.Hour)
	}
	id := onlyJob(t, e.queued("failed")).ID

	if _, err := e.runner.Retry("no-such-job"); !errors.Is(err, actions.ErrNoJob) {
		t.Fatalf("unknown job: err = %v, want ErrNoJob", err)
	}
	if n, err := e.runner.Retry(id); err != nil || n != 1 {
		t.Fatalf("Retry = %d, %v", n, err)
	}
	if len(e.queued("failed")) != 0 {
		t.Fatal("a retried job must leave failed")
	}
	if job := onlyJob(t, e.queued("")); job.Attempt != 0 || job.LastError != "" {
		t.Fatalf("retried job must start over, got %+v", job)
	}

	e.hook.fail(0)
	e.runner.RunDue(context.Background())
	if len(e.queued("")) != 0 || len(e.queued("failed")) != 0 {
		t.Fatal("the retried job must run and finish")
	}
	if n, err := e.runner.Retry(""); err != nil || n != 0 {
		t.Fatalf("Retry all with nothing failed = %d, %v", n, err)
	}
}

func TestQueueSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	e.upload("/hook", "a.csv", []byte("x"))

	// A new runner on the same folder stands in for a restarted process.
	buckets := storage.NewRegistry()
	_ = buckets.Prepare(context.Background(), e.cfg.Load())
	restarted, err := actions.NewRunner(filepath.Join(e.dir, "queue"), e.dir, e.cfg.Load, buckets)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Now = func() time.Time { return e.now }

	// The mem:// bucket of the new registry is empty, so copy the file over.
	data, _ := e.raw.ReadAll(context.Background(), "hook/a.csv")
	_, _ = storage.PutFile(context.Background(), buckets.Bucket(e.cfg.Load(), "raw"), "hook/a.csv", bytes.NewReader(data), int64(len(data)), storage.PutOptions{})

	restarted.RunDue(context.Background())
	if len(e.hook.calls()) != 1 || len(e.queued("")) != 0 {
		t.Fatal("the restarted runner must finish the queued job")
	}
}

func TestChangedFileIsNotProcessed(t *testing.T) {
	e := newEnv(t)
	e.upload("/hook", "a.csv", []byte("original"))
	// Someone replaces the object before the job runs.
	_ = e.raw.WriteAll(context.Background(), "hook/a.csv", []byte("replaced"), nil)

	e.runner.RunDue(context.Background())
	if len(e.hook.calls()) != 0 {
		t.Fatal("a changed file must not be sent on")
	}
	job := onlyJob(t, e.queued("failed"))
	if !strings.Contains(job.LastError, "changed after it was uploaded") {
		t.Fatalf("last error = %q", job.LastError)
	}
}

func TestRemovedFolderGivesUpAtOnce(t *testing.T) {
	e := newEnv(t)
	e.upload("/hook", "a.csv", []byte("x"))

	src := string(e.cfg.Load().Raw)
	cut := strings.Index(src, "      - path: /hook")
	if cut < 0 {
		t.Fatal("test setup: /hook folder not found in config")
	}
	src = src[:cut]
	e.load(src)

	e.runner.RunDue(context.Background())
	if len(e.queued("failed")) != 1 {
		t.Fatal("a job for a removed folder can never succeed and must fail at once")
	}
}

func TestStepValidation(t *testing.T) {
	dir := t.TempDir()
	writeKeys(t, dir)
	t.Setenv("PGP_PASS", passphrase)
	t.Setenv("WRONG_PASS", "nope")
	base := strings.NewReplacer(
		"HOST_KEY_FILE", filepath.Join(dir, "host_key"),
		"PRIVATE_KEY_FILE", filepath.Join(dir, "private.asc"),
		"PUBLIC_KEY_FILE", filepath.Join(dir, "public.asc"),
		"SIGNER_KEY_FILE", filepath.Join(dir, "public.asc"),
		"HOOK_URL", "https://hooks.example.com/x",
	).Replace(configTemplate)
	t.Setenv("HOOK_SECRET", hookSecret)

	cases := []struct {
		name, from, to, want string
	}{
		{"inline pgp key", `key: "${file:` + filepath.Join(dir, "private.asc") + `}"`, `key: "-----BEGIN PGP PRIVATE KEY BLOCK-----"`, "inline secret not allowed"},
		{"public key cannot decrypt", "private.asc}", "public.asc}", "decrypting needs the private key"},
		{"wrong passphrase", "${env:PGP_PASS}", "${env:WRONG_PASS}", "cannot unlock the private key"},
		{"inline webhook secret", `secret: "${env:HOOK_SECRET}"`, `secret: "hunter2"`, "inline secret not allowed"},
		{"unknown placeholder", "plain/{account}", "plain/{user}", "unknown placeholder {user}"},
		{"unknown copy storage", "copy: { storage: out, key: \"plain", "copy: { storage: nope, key: \"plain", `unknown storage "nope"`},
		{"bad webhook url", `url: "https://hooks.example.com/x", secret`, `url: "ftp://x", secret`, "must be an http or https URL"},
		{"two steps in one item", "- webhook: { url: \"https://hooks.example.com/x\" }", "- webhook: { url: \"https://hooks.example.com/x\" }\n            copy: { storage: out, key: x }", "exactly one of"},
		{"bad timezone", "timezone: Asia/Kolkata", "timezone: Mars/Base", "unknown time zone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(base, tc.from) {
				t.Fatalf("test setup: %q not in config", tc.from)
			}
			src := strings.Replace(base, tc.from, tc.to, 1)
			_, err := config.Parse([]byte(src), config.Options{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// onlyJob reads the single job file in files.
func onlyJob(t *testing.T, files []string) actions.Job {
	t.Helper()
	if len(files) != 1 {
		t.Fatalf("want 1 job file, found %d", len(files))
	}
	var job actions.Job
	b, _ := os.ReadFile(files[0])
	if err := json.Unmarshal(b, &job); err != nil {
		t.Fatal(err)
	}
	return job
}

// writeKeys writes an SSH host key and a PGP key pair into dir. The PGP
// private key is protected with the test passphrase, like a real one would
// be. It returns the unlocked PGP key for the test's own use.
func writeKeys(t *testing.T, dir string) *openpgp.Entity {
	t.Helper()
	_, hostKey, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(hostKey, "")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "host_key"), pem.EncodeToMemory(block))

	entity, err := openpgp.NewEntity("SFTPgate test", "", "test@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "public.asc"), armored(t, openpgp.PublicKeyType, entity.Serialize))

	if err := entity.EncryptPrivateKeys([]byte(passphrase), nil); err != nil {
		t.Fatal(err)
	}
	private := armored(t, openpgp.PrivateKeyType, func(w io.Writer) error {
		return entity.SerializePrivateWithoutSigning(w, nil)
	})
	writeFile(t, filepath.Join(dir, "private.asc"), private)

	keys, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(private))
	if err != nil {
		t.Fatal(err)
	}
	if err := keys[0].DecryptPrivateKeys([]byte(passphrase)); err != nil {
		t.Fatal(err)
	}
	return keys[0]
}

func armored(t *testing.T, blockType string, write func(io.Writer) error) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, blockType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := write(w); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	return buf.Bytes()
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// encrypt returns data encrypted for entity, as a partner would send it.
func encrypt(t *testing.T, entity *openpgp.Entity, data string) []byte {
	t.Helper()
	return encryptSigned(t, entity, nil, data)
}

// encryptSigned is encrypt, also signed by signer when it is not nil.
func encryptSigned(t *testing.T, entity, signer *openpgp.Entity, data string) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := openpgp.Encrypt(&out, openpgp.EntityList{entity}, signer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(data))
	_ = w.Close()
	return out.Bytes()
}

// newPGPEntity makes an unprotected PGP key pair, like a partner's signing key.
func newPGPEntity(t *testing.T) *openpgp.Entity {
	t.Helper()
	e, err := openpgp.NewEntity("Partner", "", "partner@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// hookServer records webhook calls and can fail the first few.
type hookServer struct {
	*httptest.Server
	mu        sync.Mutex
	received  []hookCall
	failsLeft int
}

type hookCall struct {
	body      []byte
	signature string
	header    http.Header
}

func newHookServer(t *testing.T) *hookServer {
	h := &hookServer{}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		defer h.mu.Unlock()
		h.received = append(h.received, hookCall{body: body, signature: r.Header.Get("X-Sftpgate-Signature"), header: r.Header.Clone()})
		if h.failsLeft > 0 {
			h.failsLeft--
			http.Error(w, "try later", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *hookServer) fail(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failsLeft = n
}

func (h *hookServer) calls() []hookCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hookCall{}, h.received...)
}

// A check in an after_* hook runs on the decrypted file. Retrying can't fix a
// bad file, so the job gives up at once, and nothing after the check runs.
func TestCheckInAfterHook(t *testing.T) {
	e := newEnv(t)
	e.upload("/checked", "good.csv.pgp", encrypt(t, e.entity, "id,amount\n1,100\n"))
	e.upload("/checked", "bad.csv.pgp", encrypt(t, e.entity, "id,amount\n"))
	e.runner.RunDue(context.Background())

	if _, ok := e.read(e.out, "plain/acme/2026-10-25/good.csv"); !ok {
		t.Fatal("a file that passes its check must be copied")
	}
	if _, ok := e.read(e.out, "plain/acme/2026-10-25/bad.csv"); ok {
		t.Fatal("actions after a failed check must not run")
	}
	job := onlyJob(t, e.queued("failed"))
	if job.Attempt != 1 || !strings.Contains(job.LastError, "check: CSV has 0 rows after the header") {
		t.Fatalf("failed job = %+v", job)
	}
}

// A hook with only webhooks doesn't download the file, but still refuses to
// announce one that was replaced. after_download reports event "download",
// and custom headers are sent.
func TestWebhookOnlyHook(t *testing.T) {
	e := newEnv(t)
	sha, err := storage.PutFile(context.Background(), e.raw, "out/report.csv", strings.NewReader("data"), 4, storage.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	err = e.runner.Add(vfs.FileEvent{
		Hook: config.AfterDownload, Account: "acme", Folder: "/out", Storage: "raw",
		Key: "out/report.csv", Filename: "report.csv", Size: 4, SHA256: sha, Time: e.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.runner.RunDue(context.Background())

	calls := e.hook.calls()
	if len(calls) != 1 {
		t.Fatalf("webhook calls = %d, want 1", len(calls))
	}
	var p actions.WebhookPayload
	_ = json.Unmarshal(calls[0].body, &p)
	if p.Event != "download" || p.Folder != "/out" || p.SHA256 != sha || !p.Time.Equal(e.now) {
		t.Fatalf("payload = %+v", p)
	}
	if calls[0].header.Get("X-Team") != "recon" || calls[0].signature == "" {
		t.Fatalf("headers = %v", calls[0].header)
	}
}

// Before runs a before_* hook right away: checks pass or reject with a
// reason, and pgp_encrypt hands back a new file and name.
func TestBefore(t *testing.T) {
	e := newEnv(t)
	cfg := e.cfg.Load()
	steps := []*config.Step{cfg.Actions["recon-csv"], cfg.Actions["encrypt"]}
	good := filepath.Join(e.dir, "good.csv")
	writeFile(t, good, []byte("id,amount\n1,100\n"))

	out, name, cleanup, err := e.runner.Before(context.Background(), steps, good, "good.csv")
	if err != nil {
		t.Fatal(err)
	}
	if name != "good.csv.pgp" || name != actions.OutName(steps, "good.csv") {
		t.Fatalf("name = %q", name)
	}
	enc, _ := os.ReadFile(out)
	md, err := openpgp.ReadMessage(bytes.NewReader(enc), openpgp.EntityList{e.entity}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := io.ReadAll(md.UnverifiedBody); string(plain) != "id,amount\n1,100\n" {
		t.Fatalf("decrypts to %q", plain)
	}
	cleanup()
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("cleanup must remove the encrypted copy")
	}
	if b, _ := os.ReadFile(good); string(b) != "id,amount\n1,100\n" {
		t.Fatal("the original file must not change")
	}

	bad := filepath.Join(e.dir, "bad.csv")
	writeFile(t, bad, []byte("name\nx\n"))
	_, _, cleanup, err = e.runner.Before(context.Background(), steps, bad, "bad.csv")
	cleanup()
	var rej *actions.Rejected
	if !errors.As(err, &rej) || !strings.Contains(rej.Reason, `CSV header is ["name"], expected ["id" "amount"]`) {
		t.Fatalf("err = %v, want a rejection with the reason", err)
	}
}

// Each rule of a check, on its own.
func TestCheckRules(t *testing.T) {
	e := newEnv(t)
	csvCheck := func(c config.CSVCheck) *config.Check { return &config.Check{CSV: &c} }
	cases := []struct {
		name  string
		check *config.Check
		data  string
		want  string // "" means the file passes
	}{
		{"min size ok", &config.Check{MinSize: 3}, "abc", ""},
		{"too small", &config.Check{MinSize: 4}, "abc", "below the minimum of 4"},
		{"too big", &config.Check{MaxSize: 2}, "abc", "above the maximum of 2"},
		{"header ok", csvCheck(config.CSVCheck{Header: []string{"id", "amount"}}), "id,amount\n", ""},
		{"spreadsheet BOM", csvCheck(config.CSVCheck{Header: []string{"id", "amount"}}), "\ufeffid,amount\n1,2\n", ""},
		{"wrong header", csvCheck(config.CSVCheck{Header: []string{"id", "amount"}}), "id,total\n", "CSV header is"},
		{"enough rows", csvCheck(config.CSVCheck{MinRows: 2}), "h\n1\n2\n", ""},
		{"too few rows", csvCheck(config.CSVCheck{MinRows: 2}), "h\n1\n", "1 rows after the header, expected at least 2"},
		{"ragged row", csvCheck(config.CSVCheck{}), "a,b\n1,2\n3\n", "wrong number of fields"},
		{"empty file", csvCheck(config.CSVCheck{}), "", "file is empty"},
		{"delimiter", csvCheck(config.CSVCheck{Header: []string{"id", "amount"}, Delimiter: ";"}), "id;amount\n1;2\n", ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := filepath.Join(e.dir, fmt.Sprintf("check-%d", i))
			writeFile(t, f, []byte(tc.data))
			_, _, cleanup, err := e.runner.Before(context.Background(), []*config.Step{{Check: tc.check}}, f, "f.csv")
			cleanup()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("want pass, got %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// Jobs queued by v0.3.0 have no hook. After an upgrade they still run, as
// after_upload jobs.
func TestJobFromOlderVersion(t *testing.T) {
	e := newEnv(t)
	e.upload("/hook", "a.csv", []byte("x"))
	f := e.queued("")[0]
	var job map[string]any
	b, _ := os.ReadFile(f)
	_ = json.Unmarshal(b, &job)
	delete(job, "hook")
	b, _ = json.Marshal(job)
	writeFile(t, f, b)

	e.runner.RunDue(context.Background())
	if len(e.hook.calls()) != 1 || len(e.queued("")) != 0 {
		t.Fatal("an older job must run as after_upload")
	}
}

// A key that can only sign can't be encrypted for. The config is refused
// when it loads, not when a transfer tries to send.
func TestSignOnlyRecipientRejected(t *testing.T) {
	dir := t.TempDir()
	writeKeys(t, dir)
	signOnly := newPGPEntity(t)
	signOnly.Subkeys = nil
	writeFile(t, filepath.Join(dir, "sign-only.asc"), armored(t, openpgp.PublicKeyType, signOnly.Serialize))
	t.Setenv("PGP_PASS", passphrase)
	t.Setenv("HOOK_SECRET", hookSecret)

	src := strings.NewReplacer(
		"HOST_KEY_FILE", filepath.Join(dir, "host_key"),
		"PRIVATE_KEY_FILE", filepath.Join(dir, "private.asc"),
		"PUBLIC_KEY_FILE", filepath.Join(dir, "sign-only.asc"),
		"SIGNER_KEY_FILE", filepath.Join(dir, "public.asc"),
		"HOOK_URL", "https://hooks.example.com/x",
	).Replace(configTemplate)
	_, err := config.Parse([]byte(src), config.Options{})
	if err == nil || !strings.Contains(err.Error(), "actions.encrypt.pgp_encrypt.recipients: key") || !strings.Contains(err.Error(), "no usable encryption key") {
		t.Fatalf("want a sign-only recipient refused, got %v", err)
	}
}
