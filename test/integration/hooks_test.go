package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"github.com/shashankpatil-dev/sftpgate/internal/actions"
)

// withHooks gives keyuser's folder a before_upload CSV check and an
// after_download webhook, as named actions.
func withHooks(hookURL string) func(string) string {
	return func(s string) string {
		s = strings.Replace(s,
			"prefix: key, perms: [list, upload, download] }",
			"prefix: key, perms: [list, upload, download], hooks: { before_upload: [recon-csv], after_download: [notify] } }", 1)
		return s + `
actions:
  recon-csv: { check: { csv: { header: [id, amount], min_rows: 1 } } }
  notify: { webhook: { url: "` + hookURL + `" } }
`
	}
}

// A file that fails a before_upload check is refused over SFTP with the
// reason, and never reaches the bucket. A good file goes through.
func TestBeforeUploadRejectsBadFile(t *testing.T) {
	h := newHarness(t, withHooks("http://127.0.0.1:1/unused"))
	c, err := h.dial("keyuser", h.withKey())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sc, err := sftp.NewClient(c)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	err = sftpPut(sc, "/data/bad.csv", "name,total\nx,1\n")
	if err == nil || !strings.Contains(err.Error(), "upload rejected: CSV header is") {
		t.Fatalf("bad file: err = %v, want a rejection with the reason", err)
	}
	if _, err := sc.Stat("/data/bad.csv"); err == nil {
		t.Fatal("a rejected file must not be stored")
	}
	h.audit.waitFor(t, `"result":"denied"`)

	if err := sftpPut(sc, "/data/good.csv", "id,amount\n1,10\n"); err != nil {
		t.Fatalf("good file: %v", err)
	}
	if _, err := sc.Stat("/data/good.csv"); err != nil {
		t.Fatalf("good file not stored: %v", err)
	}
}

// A finished download runs the folder's after_download webhook. A webhook
// alone doesn't fetch the file again, and the SHA-256 stored at upload is sent.
func TestAfterDownloadWebhook(t *testing.T) {
	got := make(chan actions.WebhookPayload, 1)
	hook := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var p actions.WebhookPayload
		_ = json.NewDecoder(r.Body).Decode(&p)
		got <- p
	}))
	defer hook.Close()

	h := newHarness(t, withHooks(hook.URL))
	c, err := h.dial("keyuser", h.withKey())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sc, err := sftp.NewClient(c)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	if err := sftpPut(sc, "/data/report.csv", "id,amount\n1,10\n"); err != nil {
		t.Fatal(err)
	}
	f, err := sc.Open("/data/report.csv")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(f)
	_ = f.Close()

	select {
	case p := <-got:
		if p.Event != "download" || p.Account != "keyuser" || p.Key != "key/report.csv" || p.SHA256 == "" {
			t.Fatalf("webhook payload = %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("after_download webhook was not called")
	}
}

func sftpPut(sc *sftp.Client, name, data string) error {
	f, err := sc.Create(name)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(data)); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
