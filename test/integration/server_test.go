// Package integration runs a real SFTPgate server on localhost and talks to
// it with real SSH and SFTP clients.
package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"

	"github.com/shashankpatil-dev/sftpgate/internal/actions"
	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/server"
	"github.com/shashankpatil-dev/sftpgate/internal/storage"
)

const password = "correct horse battery staple"

// harness is one running server plus the keys needed to log in.
type harness struct {
	t       *testing.T
	addr    string
	cfg     atomic.Pointer[config.Config]
	srv     *server.Server
	buckets *storage.Registry
	userKey ssh.Signer
	rsaKey  ssh.Signer
	dir     string
	audit   *syncBuffer
}

// configTemplate has four accounts:
//   - keyuser:  key only
//   - pwuser:   password only
//   - bothuser: key AND password
//   - faruser:  only allowed from 10.0.0.0/8, so never from localhost
const configTemplate = `
version: 1
server:
  host_keys: [HOSTKEY]
  spool_dir: SPOOL
  min_spool_free: 1MiB
  throttle: { max_failures: 2, window: 1m, block: 1m }
storages:
  raw: mem://
accounts:
  - name: keyuser
    max_sessions: 1
    auth: { public_keys: ["USERKEY", "RSAKEY"] }
    folders: [{ path: /data, storage: raw, prefix: key, perms: [list, upload, download] }]
  - name: pwuser
    auth: { password_hash: "${env:PW_HASH}" }
    folders: [{ path: /data, storage: raw, prefix: pw, perms: [list, upload, download] }]
  - name: bothuser
    auth: { mode: all, public_keys: ["USERKEY"], password_hash: "${env:PW_HASH}" }
    folders: [{ path: /data, storage: raw, prefix: both, perms: [list, upload, download] }]
  - name: faruser
    allow_ips: [10.0.0.0/8]
    auth: { public_keys: ["USERKEY"] }
    folders: [{ path: /data, storage: raw, prefix: far, perms: [list] }]
`

func newHarness(t *testing.T, edit func(string) string) *harness {
	t.Helper()
	h := &harness{t: t, dir: t.TempDir(), audit: &syncBuffer{}}
	audit.SetOutput(h.audit)

	writeKey(t, filepath.Join(h.dir, "host_key"))
	h.userKey = writeKey(t, filepath.Join(h.dir, "user_key"))
	rsaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	h.rsaKey, _ = ssh.NewSignerFromKey(rsaPriv)
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost+6)
	t.Setenv("PW_HASH", string(hash))

	src := configTemplate
	src = strings.ReplaceAll(src, "HOSTKEY", filepath.Join(h.dir, "host_key"))
	src = strings.ReplaceAll(src, "SPOOL", h.dir)
	src = strings.ReplaceAll(src, "USERKEY", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(h.userKey.PublicKey()))))
	src = strings.ReplaceAll(src, "RSAKEY", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(h.rsaKey.PublicKey()))))
	if edit != nil {
		src = edit(src)
	}
	h.load(src)

	h.srv = server.New(h.cfg.Load, h.buckets)
	runner, err := actions.NewRunner(filepath.Join(h.dir, "queue"), h.dir, h.cfg.Load, h.buckets)
	if err != nil {
		t.Fatal(err)
	}
	h.srv.AfterUpload = runner.Add

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.addr = ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = h.srv.Serve(ctx, ln, time.Second)
		close(done)
	}()
	runnerDone := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(runnerDone)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		<-runnerDone
	})
	return h
}

// load parses src and makes it the server's current config.
func (h *harness) load(src string) *config.Config {
	h.t.Helper()
	cfg, err := config.Parse([]byte(src), config.Options{})
	if err != nil {
		h.t.Fatal(err)
	}
	if h.buckets == nil {
		h.buckets = storage.NewRegistry()
	}
	if err := h.buckets.Prepare(context.Background(), cfg); err != nil {
		h.t.Fatal(err)
	}
	h.cfg.Store(cfg)
	return cfg
}

func (h *harness) dial(user string, methods ...ssh.AuthMethod) (*ssh.Client, error) {
	return ssh.Dial("tcp", h.addr, h.clientConfig(user, methods...))
}

func (h *harness) clientConfig(user string, methods ...ssh.AuthMethod) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:            user,
		Auth:            methods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
}

func (h *harness) withKey() ssh.AuthMethod      { return ssh.PublicKeys(h.userKey) }
func (h *harness) withPassword() ssh.AuthMethod { return ssh.Password(password) }

// sftpRoundTrip uploads a file and reads it back.
func sftpRoundTrip(t *testing.T, c *ssh.Client) {
	t.Helper()
	sc, err := sftp.NewClient(c)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	f, err := sc.Create("/data/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("hello"))
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := sc.Open("/data/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	if string(got) != "hello" {
		t.Fatalf("read back %q", got)
	}
}

func TestLoginModes(t *testing.T) {
	// Many failures on purpose here, so raise the limit to keep the IP unblocked.
	h := newHarness(t, func(s string) string {
		return strings.Replace(s, "max_failures: 2", "max_failures: 100", 1)
	})

	cases := []struct {
		name    string
		user    string
		methods []ssh.AuthMethod
		ok      bool
	}{
		{"key user with key", "keyuser", []ssh.AuthMethod{h.withKey()}, true},
		{"key user with password", "keyuser", []ssh.AuthMethod{h.withPassword()}, false},
		{"password user with password", "pwuser", []ssh.AuthMethod{h.withPassword()}, true},
		{"password user wrong password", "pwuser", []ssh.AuthMethod{ssh.Password("nope")}, false},
		{"both user with key and password", "bothuser", []ssh.AuthMethod{h.withKey(), h.withPassword()}, true},
		{"both user with key only", "bothuser", []ssh.AuthMethod{h.withKey()}, false},
		{"both user with password only", "bothuser", []ssh.AuthMethod{h.withPassword()}, false},
		{"account limited to other IPs", "faruser", []ssh.AuthMethod{h.withKey()}, false},
		{"unknown user", "ghost", []ssh.AuthMethod{h.withKey(), h.withPassword()}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := h.dial(tc.user, tc.methods...)
			if tc.ok != (err == nil) {
				t.Fatalf("login ok = %v, want %v (err %v)", err == nil, tc.ok, err)
			}
			if err == nil {
				sftpRoundTrip(t, c)
				_ = c.Close()
			}
		})
	}
}

// Old clients that only speak SHA-1 are refused on purpose. They must upgrade.
func TestSHA1IsRefused(t *testing.T) {
	h := newHarness(t, nil)

	rsaSHA1, err := ssh.NewSignerWithAlgorithms(h.rsaKey.(ssh.AlgorithmSigner), []string{ssh.KeyAlgoRSA})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		setup func(*ssh.ClientConfig)
	}{
		{"sha1 key exchange", func(c *ssh.ClientConfig) {
			c.KeyExchanges = []string{ssh.InsecureKeyExchangeDH14SHA1}
		}},
		{"sha1 mac", func(c *ssh.ClientConfig) {
			// MACs are only used with non-AEAD ciphers, so pick one.
			c.Ciphers = []string{"aes128-ctr"}
			c.MACs = []string{ssh.HMACSHA1}
		}},
		{"ssh-rsa sha1 signature", func(c *ssh.ClientConfig) {
			c.Auth = []ssh.AuthMethod{ssh.PublicKeys(rsaSHA1)}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := h.clientConfig("keyuser", h.withKey())
			tc.setup(cfg)
			if c, err := ssh.Dial("tcp", h.addr, cfg); err == nil {
				_ = c.Close()
				t.Fatal("a SHA-1 only client must be refused")
			}
		})
	}

	// Refused handshakes must be visible, so an operator can tell a partner
	// why its old software can't connect.
	h.audit.waitFor(t, `"error":"ssh handshake failed: ssh: no common algorithm for key exchange`)

	// The same RSA key works fine with SHA-2 signatures.
	c, err := h.dial("keyuser", ssh.PublicKeys(h.rsaKey))
	if err != nil {
		t.Fatalf("RSA key with SHA-2 must work: %v", err)
	}
	_ = c.Close()
}

func TestShellAndExecAreRefused(t *testing.T) {
	h := newHarness(t, nil)
	c, err := h.dial("keyuser", h.withKey())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Run("cat /etc/passwd"); err == nil {
		t.Fatal("exec must be refused")
	}

	if _, err := c.Dial("tcp", "127.0.0.1:22"); err == nil {
		t.Fatal("port forwarding must be refused")
	}
}

func TestGlobalIPAllowlist(t *testing.T) {
	h := newHarness(t, func(s string) string {
		return strings.Replace(s, "  spool_dir:", "  allow_ips: [10.0.0.0/8]\n  spool_dir:", 1)
	})
	if _, err := h.dial("keyuser", h.withKey()); err == nil {
		t.Fatal("localhost is not in the global allowlist and must be refused")
	}
	h.audit.waitFor(t, `"event":"conn_rejected"`)
}

func TestThrottleBlocksAfterFailures(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < 2; i++ {
		if _, err := h.dial("pwuser", ssh.Password("wrong")); err == nil {
			t.Fatal("wrong password must fail")
		}
	}
	// The right password is refused too now, because the IP is blocked.
	if _, err := h.dial("pwuser", h.withPassword()); err == nil {
		t.Fatal("blocked IP must be refused even with the right password")
	}
	h.audit.waitFor(t, `"error":"too many failed logins"`)
	h.audit.waitFor(t, `"user":"pwuser"`)
}

func TestSessionsList(t *testing.T) {
	h := newHarness(t, nil)
	c, err := h.dial("keyuser", h.withKey())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sftpRoundTrip(t, c)

	sessions := h.srv.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v", sessions)
	}
	s := sessions[0]
	if s.Account != "keyuser" || s.IP != "127.0.0.1" || s.Method != "publickey" || s.BytesIn != 5 || s.BytesOut != 5 {
		t.Fatalf("session = %+v", s)
	}

	_ = c.Close()
	h.audit.waitFor(t, `"event":"session_end"`)
	if len(h.srv.Sessions()) != 0 {
		t.Fatal("a closed session must leave the list")
	}
}

func TestMaxSessions(t *testing.T) {
	h := newHarness(t, nil)
	first, err := h.dial("keyuser", h.withKey())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	h.audit.waitFor(t, `"event":"session_start"`)

	second, err := h.dial("keyuser", h.withKey())
	if err == nil {
		defer second.Close()
		if _, err := sftp.NewClient(second); err == nil {
			t.Fatal("second session must be refused when max_sessions is 1")
		}
	}
}

func TestMaxConnections(t *testing.T) {
	h := newHarness(t, func(s string) string {
		return strings.Replace(s, "  spool_dir:", "  max_connections: 1\n  spool_dir:", 1)
	})
	first, err := h.dial("keyuser", h.withKey())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	if c, err := h.dial("pwuser", h.withPassword()); err == nil {
		_ = c.Close()
		t.Fatal("a second connection must be refused when max_connections is 1")
	}
	h.audit.waitFor(t, `"error":"too many connections"`)

	// Once the first connection closes, there is room again.
	_ = first.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := h.dial("pwuser", h.withPassword())
		if err == nil {
			_ = c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("connection still refused after the first one closed: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestReloadClosesOnlyChangedAccounts(t *testing.T) {
	h := newHarness(t, nil)
	keyConn, err := h.dial("keyuser", h.withKey())
	if err != nil {
		t.Fatal(err)
	}
	defer keyConn.Close()
	pwConn, err := h.dial("pwuser", h.withPassword())
	if err != nil {
		t.Fatal(err)
	}
	defer pwConn.Close()

	// Take upload permission away from pwuser only.
	src := string(h.cfg.Load().Raw)
	src = strings.Replace(src, "prefix: pw, perms: [list, upload, download]", "prefix: pw, perms: [list]", 1)
	h.srv.CloseChanged(h.load(src))

	waitClosed(t, pwConn)
	if _, err := sftp.NewClient(keyConn); err != nil {
		t.Fatalf("unchanged account must keep working: %v", err)
	}
}

func TestAuditRecordsUpload(t *testing.T) {
	h := newHarness(t, nil)
	c, err := h.dial("keyuser", h.withKey())
	if err != nil {
		t.Fatal(err)
	}
	sftpRoundTrip(t, c)
	_ = c.Close()

	line := h.audit.waitFor(t, `"event":"upload"`)
	var ev map[string]any
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatal(err)
	}
	if ev["account"] != "keyuser" || ev["result"] != "ok" || ev["key"] != "key/hello.txt" || ev["sha256"] == nil {
		t.Fatalf("upload audit = %v", ev)
	}
}

// An upload over SFTP into a folder with on_upload steps must reach the
// webhook, through the server, the queue and the background workers.
func TestUploadTriggersActions(t *testing.T) {
	got := make(chan actions.WebhookPayload, 1)
	hook := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var p actions.WebhookPayload
		_ = json.NewDecoder(r.Body).Decode(&p)
		got <- p
	}))
	defer hook.Close()

	h := newHarness(t, func(s string) string {
		return strings.Replace(s,
			"prefix: key, perms: [list, upload, download] }",
			"prefix: key, perms: [list, upload, download], on_upload: [{ webhook: { url: \""+hook.URL+"\" } }] }", 1)
	})
	c, err := h.dial("keyuser", h.withKey())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sftpRoundTrip(t, c)

	select {
	case p := <-got:
		if p.Account != "keyuser" || p.Key != "key/hello.txt" || p.Size != 5 {
			t.Fatalf("webhook payload = %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("webhook was not called")
	}
}

// waitClosed fails the test if the connection is still open after a while.
func waitClosed(t *testing.T, c *ssh.Client) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = c.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connection should have been closed")
	}
}

func writeKey(t *testing.T, p string) ssh.Signer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// syncBuffer collects audit lines from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// waitFor waits until an audit line contains text, and returns that line.
func (b *syncBuffer) waitFor(t *testing.T, text string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		lines := strings.Split(b.buf.String(), "\n")
		b.mu.Unlock()
		for _, l := range lines {
			if strings.Contains(l, text) {
				return l
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no audit line containing %s", text)
	return ""
}
