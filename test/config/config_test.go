package config_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"

	// An in-memory secret store, so ${secret:} can be tested without a cloud account.
	_ "gocloud.dev/runtimevar/constantvar"

	"github.com/shashankpatil-dev/sftpgate/internal/config"
)

const testPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl test"

func hostKeyFile(t *testing.T) string {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "host_key")
	if err := os.WriteFile(p, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// base returns a valid config; tests replace parts of it.
func base(hostKey string) string {
	return `
version: 1
server:
  host_keys: [` + hostKey + `]
  allow_ips: [10.0.0.0/8]
storages:
  raw: mem://
accounts:
  - name: razorpay
    auth:
      mode: all
      public_keys: ["` + testPubKey + `"]
      password_hash: ${env:TEST_PW_HASH}
    allow_ips: [52.66.0.0/16, 13.232.10.4]
    folders:
      - path: /inbound
        storage: raw
        prefix: razorpay
        perms: [list, upload]
        patterns: ["*.csv"]
        max_file_size: 500MiB
`
}

func setHash(t *testing.T) {
	h, _ := bcrypt.GenerateFromPassword([]byte("secret"), 10)
	t.Setenv("TEST_PW_HASH", string(h))
}

func TestValidConfig(t *testing.T) {
	setHash(t)
	cfg, err := config.Parse([]byte(base(hostKeyFile(t))), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Account("razorpay")
	if a == nil || len(a.Keys) != 1 || len(a.IPs) != 2 || len(cfg.HostKeys) != 1 {
		t.Fatalf("derived fields not filled: %+v", a)
	}
	f := a.Folder("/inbound")
	if f.Prefix != "razorpay/" || !f.Perm.Has(config.PermUpload) || f.Perm.Has(config.PermDelete) || f.MaxFileSize != 500<<20 {
		t.Fatalf("folder not normalized: %+v", f)
	}
	if cfg.Server.Listen != ":2222" || cfg.Server.Throttle.MaxFailures != 5 {
		t.Fatal("defaults not applied")
	}
	if !strings.Contains(string(cfg.Raw), "${env:TEST_PW_HASH}") {
		t.Fatal("Raw must keep refs, not secret values")
	}
}

func TestValidationErrors(t *testing.T) {
	setHash(t)
	key := hostKeyFile(t)
	cases := []struct {
		name, from, to, want string
	}{
		{"inline password", "${env:TEST_PW_HASH}", "hunter2", "accounts[0].auth.password_hash: inline secret not allowed"},
		{"bad hash", "${env:TEST_PW_HASH}", "${env:NOT_A_HASH}", "possible plaintext password"},
		{"missing env", "${env:TEST_PW_HASH}", "${env:NOPE_UNSET}", "env NOPE_UNSET not set"},
		{"unknown ref", "${env:TEST_PW_HASH}", "${vault:x}", "unsupported reference"},
		{"unknown field", "max_file_size:", "max_size:", "field max_size not found"},
		{"bad cidr", "13.232.10.4", "13.232.10.999", "accounts[0].allow_ips[1]: invalid IP"},
		{"bad perm", "[list, upload]", "[list, write]", `unknown permission "write"`},
		{"unknown storage", "storage: raw", "storage: nope", `unknown storage "nope"`},
		{"nested folder", "path: /inbound", "path: /in/bound", "single segment"},
		{"dotdot prefix", "prefix: razorpay", "prefix: ../x", "no . or .. segments"},
		{"bad scheme", "raw: mem://", "raw: ftp://x", "unsupported storage URL"},
		{"bad name", "name: razorpay", "name: Razor Pay", "accounts[0].name"},
		{"bad mode", "mode: all", "mode: weird", "must be any or all"},
		{"bad size", "500MiB", "5 TB", "invalid size"},
		{"bad pattern", `"*.csv"`, `"[x"`, "bad pattern"},
	}
	t.Setenv("NOT_A_HASH", "plaintext")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.Replace(base(key), tc.from, tc.to, 1)
			_, err := config.Parse([]byte(src), config.Options{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestModeAllNeedsBoth(t *testing.T) {
	src := strings.Replace(base(hostKeyFile(t)), "      password_hash: ${env:TEST_PW_HASH}\n", "", 1)
	_, err := config.Parse([]byte(src), config.Options{})
	if err == nil || !strings.Contains(err.Error(), "mode all requires both") {
		t.Fatalf("got %v", err)
	}
}

func TestOffline(t *testing.T) {
	// No env, no host key file: offline must still pass, and still catch inline secrets.
	src := base("/does/not/exist")
	if _, err := config.Parse([]byte(src), config.Options{Offline: true}); err != nil {
		t.Fatalf("offline should pass: %v", err)
	}
	keyFromFile := strings.Replace(src, `"`+testPubKey+`"`, `"${file:/does/not/exist.pub}"`, 1)
	if _, err := config.Parse([]byte(keyFromFile), config.Options{Offline: true}); err != nil {
		t.Fatalf("offline should accept a public key given as a file reference: %v", err)
	}

	src = strings.Replace(src, "${env:TEST_PW_HASH}", "$2a$10$abcdefghijklmnopqrstuuMx0Z3Zl5z7ZbYqvJmQmX4o1zS4xU8hG", 1)
	if _, err := config.Parse([]byte(src), config.Options{Offline: true}); err == nil || !strings.Contains(err.Error(), "inline secret") {
		t.Fatalf("offline must reject inline secrets, got %v", err)
	}
}

func TestRefCannotInjectYAML(t *testing.T) {
	setHash(t)
	t.Setenv("EVIL", "x\nversion: 2")
	src := strings.Replace(base(hostKeyFile(t)), "raw: mem://", "raw: mem://${env:EVIL}", 1)
	cfg, err := config.Parse([]byte(src), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 || cfg.Storages["raw"] != "mem://x\nversion: 2" {
		t.Fatalf("ref value leaked into structure: %+v", cfg.Storages)
	}
}

func TestFingerprintChanges(t *testing.T) {
	setHash(t)
	key := hostKeyFile(t)
	a, _ := config.Parse([]byte(base(key)), config.Options{})
	b, _ := config.Parse([]byte(strings.Replace(base(key), "[list, upload]", "[list]", 1)), config.Options{})
	c, _ := config.Parse([]byte(base(key)+"\n# comment\n"), config.Options{})
	if a.Account("razorpay").Fingerprint == b.Account("razorpay").Fingerprint {
		t.Fatal("perm change must change fingerprint")
	}
	if a.Account("razorpay").Fingerprint != c.Account("razorpay").Fingerprint {
		t.Fatal("comment-only change must not change fingerprint")
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]config.Size{"0": 0, "10": 10, "1KiB": 1024, "2 MiB": 2 << 20, "3GiB": 3 << 30, "7B": 7} {
		if got, err := config.ParseSize(in); err != nil || got != want {
			t.Errorf("%s: got %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "-1", "1TB", "abc", "1.5GiB"} {
		if _, err := config.ParseSize(in); err == nil {
			t.Errorf("%s: want error", in)
		}
	}
}

func TestSecretReference(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), 10)
	ref := "${secret:constant://?val=" + url.QueryEscape(string(hash)) + "}"
	key := hostKeyFile(t)

	src := strings.Replace(base(key), "${env:TEST_PW_HASH}", ref, 1)
	cfg, err := config.Parse([]byte(src), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Account("razorpay").Auth.PasswordHash; got != string(hash) {
		t.Fatalf("password hash from secret store = %q", got)
	}

	// A secret store that doesn't exist is reported with the field's path.
	bad := strings.Replace(base(key), "${env:TEST_PW_HASH}", "${secret:nosuchstore://x}", 1)
	_, err = config.Parse([]byte(bad), config.Options{})
	if err == nil || !strings.Contains(err.Error(), "accounts[0].auth.password_hash: secret:") {
		t.Fatalf("want a secret error, got %v", err)
	}

	// Offline, secret references are not read at all.
	if _, err := config.Parse([]byte(bad), config.Options{Offline: true}); err != nil {
		t.Fatalf("offline must not read secrets: %v", err)
	}
}

func TestHTTPTokens(t *testing.T) {
	setHash(t)
	key := hostKeyFile(t)
	withTokens := func(tokens string) string {
		return strings.Replace(base(key), "storages:", "http:\n  tokens: ["+tokens+"]\nstorages:", 1)
	}

	t.Setenv("GOOD_TOKEN", "0123456789abcdef-long")
	if _, err := config.Parse([]byte(withTokens(`"${env:GOOD_TOKEN}"`)), config.Options{}); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}

	t.Setenv("SHORT_TOKEN", "short")
	for tokens, want := range map[string]string{
		`"written-inline-in-the-file"`: "http.tokens[0]: inline secret not allowed",
		`"${env:SHORT_TOKEN}"`:         "http.tokens[0]: must be at least 16 characters",
	} {
		if _, err := config.Parse([]byte(withTokens(tokens)), config.Options{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("tokens %s: want %q, got %v", tokens, want, err)
		}
	}
}

func TestAdminValidation(t *testing.T) {
	setHash(t)
	key := hostKeyFile(t)
	withHTTP := func(h string) string {
		return strings.Replace(base(key), "storages:", "http:\n"+h+"\nstorages:", 1)
	}
	t.Setenv("READ_TOKEN", "0123456789abcdef-read")
	t.Setenv("ADMIN_TOKEN", "0123456789abcdef-admin")

	cfg, err := config.Parse([]byte(withHTTP(`  tokens: ["${env:READ_TOKEN}"]`)), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Admin.Allows(config.RunTransfer) || cfg.HTTP.Admin.Allows(config.RetryActions) {
		t.Fatal("admin calls must be off by default")
	}

	cfg, err = config.Parse([]byte(withHTTP(`  admin: { tokens: ["${env:ADMIN_TOKEN}"], allow: [run_transfer] }`)), config.Options{})
	if err != nil {
		t.Fatalf("valid admin settings rejected: %v", err)
	}
	if !cfg.HTTP.Admin.Allows(config.RunTransfer) || cfg.HTTP.Admin.Allows(config.RetryActions) {
		t.Fatal("only the listed operation may be on")
	}

	for h, want := range map[string]string{
		`  admin: { tokens: ["${env:ADMIN_TOKEN}"] }`:                                                             "http.admin.allow: list the operations",
		`  admin: { allow: [run_transfer] }`:                                                                      "http.admin.tokens: needed when http.admin.allow is set",
		`  admin: { tokens: ["${env:ADMIN_TOKEN}"], allow: [delete_all] }`:                                        `http.admin.allow[0]: unknown operation "delete_all"`,
		`  admin: { tokens: ["${env:ADMIN_TOKEN}"], allow: [run_transfer, run_transfer] }`:                        `"run_transfer" is listed twice`,
		`  admin: { tokens: ["inline-admin-token-here"], allow: [run_transfer] }`:                                 "http.admin.tokens[0]: inline secret not allowed",
		"  tokens: [\"${env:READ_TOKEN}\"]\n  admin: { tokens: [\"${env:READ_TOKEN}\"], allow: [retry_actions] }": "must differ from every read-only token",
	} {
		if _, err := config.Parse([]byte(withHTTP(h)), config.Options{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", h, want, err)
		}
	}
}

func TestTransferValidation(t *testing.T) {
	setHash(t)
	key := hostKeyFile(t)
	valid := `
remotes:
  bank:
    addr: sftp.bank.example:22
    user: acme
    host_key: "` + testPubKey + `"
    password: "${env:TEST_PW_HASH}"
transfers:
  - name: statements
    schedule: "0 6 * * *"
    pull:
      from: { remote: bank, path: /out, pattern: "*.csv" }
      to: { storage: raw, key: "bank/{date}/{filename}" }
  - name: returns
    schedule: "*/30 * * * *"
    push:
      from: { storage: raw, prefix: outbox/ }
      to: { remote: bank, path: /in }
      after: "move:sent/"
`
	if _, err := config.Parse([]byte(base(key)+valid), config.Options{}); err != nil {
		t.Fatalf("valid transfers rejected: %v", err)
	}

	cases := []struct {
		name, from, to, want string
	}{
		{"unknown remote", "remote: bank, path: /out", "remote: nope, path: /out", `unknown remote "nope"`},
		{"key without filename", "bank/{date}/{filename}", "bank/{date}/x.csv", "must contain {filename}"},
		{"unknown placeholder", "bank/{date}/{filename}", "bank/{user}/{filename}", "unknown placeholder {user}"},
		{"bad schedule", `"0 6 * * *"`, `"every morning"`, "bad schedule"},
		{"relative remote path", "path: /out", "path: out", "clean absolute path"},
		{"bad after", `after: "move:sent/"`, `after: "archive"`, "must be keep, delete or move:<prefix>"},
		{"move to same prefix", `after: "move:sent/"`, `after: "move:outbox/"`, "different from from.prefix"},
		{"inline remote password", `password: "${env:TEST_PW_HASH}"`, `password: "hunter2"`, "inline secret not allowed"},
		{"no host key", `host_key: "` + testPubKey + `"`, `host_key: ""`, "must be the remote's public host key"},
		{"pull and push in one", "    pull:\n      from: { remote: bank, path: /out", "    push:\n      from: { storage: raw }\n      to: { remote: bank, path: /in }\n    pull:\n      from: { remote: bank, path: /out", "needs exactly one of pull or push"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(valid, tc.from) {
				t.Fatalf("test setup: %q not found", tc.from)
			}
			src := base(key) + strings.Replace(valid, tc.from, tc.to, 1)
			if _, err := config.Parse([]byte(src), config.Options{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRevisionFromGitSyncPath(t *testing.T) {
	// git-sync layout: /git/current -> /git/.worktrees/<commit>
	root := t.TempDir()
	commit := "3d9143ef3f90ffbbd2234ec7bb398beee70ada25"
	worktree := filepath.Join(root, ".worktrees", commit)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "sftpgate.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(worktree, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SFTPGATE_REVISION", "")
	if got := config.Revision(filepath.Join(root, "current", "sftpgate.yaml")); got != commit {
		t.Fatalf("revision = %q, want %s", got, commit)
	}
	if got := config.Revision(hostKeyFile(t)); got != "" {
		t.Fatalf("a plain path has no revision, got %q", got)
	}
	t.Setenv("SFTPGATE_REVISION", "from-ci")
	if got := config.Revision(filepath.Join(root, "current", "sftpgate.yaml")); got != "from-ci" {
		t.Fatalf("SFTPGATE_REVISION must win, got %q", got)
	}
}

func TestSourceHashSeesReferencedFiles(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "pw_hash")
	if err := os.WriteFile(secret, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := []byte("password_hash: ${file:" + secret + "}\n")

	before := config.SourceHash(raw)
	if config.SourceHash(raw) != before {
		t.Fatal("the hash must be stable")
	}
	if err := os.WriteFile(secret, []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if config.SourceHash(raw) == before {
		t.Fatal("rotating a referenced file must change the hash")
	}
}

func TestHooksValidation(t *testing.T) {
	setHash(t)
	t.Setenv("HOOK_AUTH", "Bearer abc")
	key := hostKeyFile(t)
	// The folder and both transfers use named actions through their hooks.
	valid := `
actions:
  recon-csv: { check: { min_size: 1KiB, csv: { header: [id, amount], min_rows: 1 } } }
  encrypt-ey: { pgp_encrypt: { recipients: ["${env:TEST_PW_HASH}"] } }
  archive: { copy: { storage: raw, key: "archive/{transfer}/{date}/{filename}" } }
  notify: { webhook: { url: "https://recon.example/hook", headers: { Authorization: "${env:HOOK_AUTH}", X-Team: recon } } }
remotes:
  bank:
    addr: sftp.bank.example:22
    user: acme
    host_key: "` + testPubKey + `"
    password: "${env:TEST_PW_HASH}"
transfers:
  - name: statements
    schedule: "0 6 * * *"
    pull:
      from: { remote: bank, path: /out }
      to: { storage: raw, key: "bank/{filename}" }
    hooks: { before_pull: [recon-csv], after_pull: [archive, notify] }
  - name: returns
    schedule: "0 10 * * *"
    push:
      from: { storage: raw, prefix: outbox/ }
      to: { remote: bank, path: /in }
      after: "move:sent/"
    hooks: { before_push: [recon-csv], after_push: [archive, notify] }
`
	folderHooks := "        max_file_size: 500MiB\n        hooks: { before_upload: [recon-csv], after_upload: [archive, notify], after_download: [notify] }"
	src := strings.Replace(base(key), "        max_file_size: 500MiB", folderHooks, 1) + valid
	// Offline: the encryption key reference has no real value to parse.
	if _, err := config.Parse([]byte(src), config.Options{Offline: true}); err != nil {
		t.Fatalf("valid hooks rejected: %v", err)
	}

	cases := []struct {
		name, from, to, want string
	}{
		{"unknown action", "after_upload: [archive, notify]", "after_upload: [archive, nope]", `unknown action "nope"`},
		{"webhook before upload", "before_upload: [recon-csv]", "before_upload: [notify]", "before_upload can't run notify (notify is a webhook)"},
		{"copy before push", "before_push: [recon-csv]", "before_push: [archive]", "before_push can't run archive"},
		{"encrypt before pull", "before_pull: [recon-csv]", "before_pull: [encrypt-ey]", "before_pull can't run encrypt-ey"},
		{"push hook on a folder", "after_download: [notify]", "after_push: [notify]", "hooks.after_push: not available here"},
		{"pull hook on a push", "before_push: [recon-csv]", "before_pull: [recon-csv]", "transfers[1].hooks.before_pull: not available here"},
		{"copy after delete", `after: "move:sent/"`, `after: "delete"`, "only webhook actions can run"},
		{"empty check", "recon-csv: { check: { min_size: 1KiB, csv: { header: [id, amount], min_rows: 1 } } }", "recon-csv: { check: {} }", "needs at least one of min_size, max_size, csv"},
		{"min above max", "min_size: 1KiB,", "min_size: 2KiB, max_size: 1KiB,", "min_size is bigger than max_size"},
		{"two kinds in one", "archive: { copy:", "archive: { webhook: { url: \"https://x\" }, copy:", "exactly one of"},
		{"inline auth header", `Authorization: "${env:HOOK_AUTH}"`, `Authorization: "Bearer abc"`, "headers.Authorization: inline secret not allowed"},
		{"reserved header", "X-Team: recon", "X-Sftpgate-Job: x", "set by SFTPgate"},
		{"bad action name", "  notify: {", "  Notify!: {", "name must match"},
		{"unknown placeholder", "{transfer}", "{partner}", "unknown placeholder {partner}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(src, tc.from) {
				t.Fatalf("test setup: %q not found", tc.from)
			}
			bad := strings.Replace(src, tc.from, tc.to, 1)
			if _, err := config.Parse([]byte(bad), config.Options{Offline: true}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// Files written before named actions keep working, or say exactly what to change.
func TestOlderActionSettings(t *testing.T) {
	setHash(t)
	key := hostKeyFile(t)
	onUpload := "        max_file_size: 500MiB\n        on_upload: [{ webhook: { url: \"https://x/hook\" } }]"
	src := strings.Replace(base(key), "        max_file_size: 500MiB", onUpload, 1)

	cfg, err := config.Parse([]byte(src), config.Options{})
	if err != nil {
		t.Fatalf("on_upload must still work: %v", err)
	}
	if steps := cfg.Account("razorpay").Folder("/inbound").Hooks.Steps(config.AfterUpload); len(steps) != 1 || steps[0].Webhook == nil {
		t.Fatalf("on_upload should become after_upload, got %v", steps)
	}

	both := strings.Replace(src, "on_upload:", "hooks: { after_upload: [n] }\n        on_upload:", 1) + "actions:\n  n: { webhook: { url: \"https://x\" } }\n"
	if _, err := config.Parse([]byte(both), config.Options{}); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("on_upload with hooks.after_upload: got %v", err)
	}

	queue := src + "actions:\n  workers: 2\n"
	if _, err := config.Parse([]byte(queue), config.Options{}); err == nil || !strings.Contains(err.Error(), "actions.workers moved to server.queue.workers") {
		t.Fatalf("old queue settings: got %v", err)
	}
}

// A secret reused with a YAML alias is a reference at every place it is
// used. It used to count as an inline secret everywhere but the first.
func TestAliasedSecret(t *testing.T) {
	setHash(t)
	t.Setenv("HOOK_SECRET", "s3cret")
	key := hostKeyFile(t)
	src := strings.Replace(base(key), "        max_file_size: 500MiB",
		"        max_file_size: 500MiB\n        on_upload: [{ webhook: &hook { url: \"https://x/hook\", secret: \"${env:HOOK_SECRET}\" } }]\n"+
			"      - { path: /other, storage: raw, prefix: other, perms: [upload], on_upload: [{ webhook: *hook }] }", 1)
	cfg, err := config.Parse([]byte(src), config.Options{})
	if err != nil {
		t.Fatalf("aliased secret rejected: %v", err)
	}
	if got := cfg.Account("razorpay").Folder("/other").Hooks.Steps(config.AfterUpload)[0].Webhook.Secret; got != "s3cret" {
		t.Fatalf("aliased secret = %q", got)
	}
}
