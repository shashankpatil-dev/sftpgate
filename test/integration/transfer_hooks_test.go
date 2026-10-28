package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"

	"github.com/shashankpatil-dev/sftpgate/internal/actions"
)

// hookedTransfers wires named actions into the push-and-move and pull-csv
// transfers: a CSV check before both, encryption before the push, and a
// webhook after both.
func hookedTransfers(hookURL, publicKeyFile string) func(string) string {
	return func(s string) string {
		s = strings.Replace(s, `      after: "move:sent/"`,
			"      after: \"move:sent/\"\n    hooks: { before_push: [recon-csv, encrypt], after_push: [notify] }", 1)
		s = strings.Replace(s, `      to: { storage: mine, key: "in/{date}/{filename}" }`,
			"      to: { storage: mine, key: \"in/{date}/{filename}\" }\n    hooks: { before_pull: [recon-csv], after_pull: [notify] }", 1)
		return s + `
actions:
  recon-csv: { check: { csv: { header: [id, amount], min_rows: 1 } } }
  encrypt: { pgp_encrypt: { recipients: ["${file:` + publicKeyFile + `}"] } }
  notify: { webhook: { url: "` + hookURL + `" } }
`
	}
}

// hookedEnv is a transfer env whose scheduler runs hooks through a real
// action runner, plus a webhook receiver.
type hookedEnv struct {
	*transferEnv
	runner *actions.Runner
	entity *openpgp.Entity
	mu     sync.Mutex
	got    []actions.WebhookPayload
}

func newHookedEnv(t *testing.T) *hookedEnv {
	t.Helper()
	he := &hookedEnv{}
	hook := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var p actions.WebhookPayload
		_ = json.NewDecoder(r.Body).Decode(&p)
		he.mu.Lock()
		he.got = append(he.got, p)
		he.mu.Unlock()
	}))
	t.Cleanup(hook.Close)

	entity, err := openpgp.NewEntity("EY", "", "ey@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	he.entity = entity
	var pub bytes.Buffer
	w, _ := armor.Encode(&pub, openpgp.PublicKeyType, nil)
	_ = entity.Serialize(w)
	_ = w.Close()
	pubFile := filepath.Join(t.TempDir(), "ey.asc")
	if err := os.WriteFile(pubFile, pub.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	he.transferEnv = newTransferEnv(t, hookedTransfers(hook.URL, pubFile))
	// The runner shares the scheduler's buckets: in-memory buckets hold the
	// files only inside one registry.
	he.runner, err = actions.NewRunner(filepath.Join(t.TempDir(), "queue"), t.TempDir(), he.sched.Config, he.sched.Buckets)
	if err != nil {
		t.Fatal(err)
	}
	he.sched.Before, he.sched.Enqueue = he.runner.Before, he.runner.Add
	return he
}

func (he *hookedEnv) webhooks() []actions.WebhookPayload {
	he.runner.RunDue(context.Background())
	he.mu.Lock()
	defer he.mu.Unlock()
	return append([]actions.WebhookPayload{}, he.got...)
}

// before_push checks every file and encrypts it; only good files are sent,
// under the encrypted name. A bad file stays in the outbox and fails the run.
func TestPushHooks(t *testing.T) {
	e := newHookedEnv(t)
	put(t, e.mine, "outbox/good.csv", "id,amount\n1,10\n")
	put(t, e.mine, "outbox/bad.csv", "name\nx\n")

	err := e.sched.RunNow(context.Background(), "push-and-move")
	if err == nil || !strings.Contains(err.Error(), "1 of 2 files failed") {
		t.Fatalf("run error = %v, want one failed file", err)
	}

	sent, ok := read(e.partner, "key/good.csv.pgp")
	if !ok {
		t.Fatal("the good file must reach the partner encrypted, as good.csv.pgp")
	}
	md, err := openpgp.ReadMessage(strings.NewReader(sent), openpgp.EntityList{e.entity}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := io.ReadAll(md.UnverifiedBody); string(plain) != "id,amount\n1,10\n" {
		t.Fatalf("partner decrypts to %q", plain)
	}
	if _, ok := read(e.partner, "key/good.csv"); ok {
		t.Fatal("the plain file must never be sent")
	}
	for _, k := range []string{"key/bad.csv", "key/bad.csv.pgp"} {
		if _, ok := read(e.partner, k); ok {
			t.Fatalf("%s: a file that fails its check must not be sent", k)
		}
	}
	if _, ok := read(e.mine, "outbox/bad.csv"); !ok {
		t.Fatal("a rejected file stays in the outbox")
	}

	hooks := e.webhooks()
	if len(hooks) != 1 {
		t.Fatalf("want 1 webhook (for good.csv), got %+v", hooks)
	}
	p := hooks[0]
	if p.Event != "push" || p.Transfer != "push-and-move" || p.Remote != "partner" || p.RemotePath != "/data/good.csv.pgp" || p.Key != "sent/good.csv" {
		t.Fatalf("webhook payload = %+v", p)
	}
}

// before_pull checks every pulled file before it is stored. A bad file is
// neither stored nor deleted on the partner, and fails the run.
func TestPullHooks(t *testing.T) {
	e := newHookedEnv(t)
	put(t, e.partner, "key/good.csv", "id,amount\n1,10\n")
	put(t, e.partner, "key/bad.csv", "id,amount\n")

	err := e.sched.RunNow(context.Background(), "pull-csv")
	if err == nil || !strings.Contains(err.Error(), "1 of 2 files failed") {
		t.Fatalf("run error = %v, want one failed file", err)
	}
	if _, ok := read(e.mine, "in/2026-10-25/good.csv"); !ok {
		t.Fatal("the good file must be stored")
	}
	if _, ok := read(e.mine, "in/2026-10-25/bad.csv"); ok {
		t.Fatal("a file that fails its check must not be stored")
	}
	if _, ok := read(e.partner, "key/bad.csv"); !ok {
		t.Fatal("a rejected file stays on the partner")
	}

	hooks := e.webhooks()
	if len(hooks) != 1 {
		t.Fatalf("want 1 webhook (for good.csv), got %+v", hooks)
	}
	if p := hooks[0]; p.Event != "pull" || p.Transfer != "pull-csv" || p.RemotePath != "/data/good.csv" || p.Key != "in/2026-10-25/good.csv" || p.SHA256 == "" {
		t.Fatalf("webhook payload = %+v", p)
	}
}
