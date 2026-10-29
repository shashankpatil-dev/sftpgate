package web_test

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shashankpatil-dev/sftpgate/internal/actions"
	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/transfers"
	"github.com/shashankpatil-dev/sftpgate/internal/web"
)

const adminToken = "a-separate-admin-token"

func post(t *testing.T, url string, auth func(*http.Request)) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, nil)
	if auth != nil {
		auth(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

// adminSite allows ops; started and retried record the calls that got through.
func adminSite(t *testing.T, ops []string, started *[]string, retried *[]string) string {
	h := config.HTTP{Tokens: []string{token}, Admin: config.Admin{Tokens: []string{adminToken}, Allow: ops}}
	return newSiteWith(t, h, func(s *web.Source) {
		s.RunTransfer = func(name string) error {
			switch name {
			case "busy":
				return transfers.ErrBusy
			case "missing":
				return transfers.ErrNoTransfer
			}
			*started = append(*started, name)
			return nil
		}
		s.RetryActions = func(id string) (int, error) {
			if id == "gone" {
				return 0, actions.ErrNoJob
			}
			*retried = append(*retried, id)
			return 3, nil
		}
	}).URL
}

// Admin calls don't exist unless admin tokens are set and the call is allowed.
func TestAdminIsOffByDefault(t *testing.T) {
	var started, retried []string
	cases := map[string]string{
		"no admin settings": newSite(t, []string{token}).URL,
		"only the other op": adminSite(t, []string{config.RetryActions}, &started, &retried),
	}
	for name, url := range cases {
		for _, auth := range []func(*http.Request){bearer(token), bearer(adminToken)} {
			if resp, _ := post(t, url+"/api/v1/transfers/t1/run", auth); resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s: status %d, want 404", name, resp.StatusCode)
			}
		}
	}
	if resp, _ := post(t, newSite(t, []string{token}).URL+"/api/v1/actions/retry", bearer(token)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("retry without admin settings: status %d, want 404", resp.StatusCode)
	}
	if len(started) > 0 {
		t.Fatalf("nothing may run, ran %v", started)
	}
}

// Only an admin token as a Bearer header works: not a read-only token, and
// not a Basic login a browser could send on its own. Refusals are audited.
func TestAdminToken(t *testing.T) {
	var buf bytes.Buffer
	var started, retried []string
	url := adminSite(t, []string{config.RunTransfer}, &started, &retried)
	audit.SetOutput(&buf)
	defer audit.SetOutput(io.Discard)

	for name, auth := range map[string]func(*http.Request){
		"none":            nil,
		"read-only token": bearer(token),
		"basic login":     basic(adminToken),
		"wrong":           bearer("wrong-token-of-some-length"),
	} {
		if resp, _ := post(t, url+"/api/v1/transfers/t1/run", auth); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, resp.StatusCode)
		}
	}
	if len(started) > 0 {
		t.Fatalf("nothing may run without the admin token, ran %v", started)
	}
	if !strings.Contains(buf.String(), `"event":"admin"`) || !strings.Contains(buf.String(), `"result":"denied"`) {
		t.Fatalf("refused admin calls must be audited: %s", buf.String())
	}

	// The read-only API doesn't take the admin token: they are separate.
	if resp, _ := get(t, url+"/api/v1/status", bearer(adminToken)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("admin token on the read-only API: status %d, want 401", resp.StatusCode)
	}
	// Admin calls are POST only.
	if resp, _ := get(t, url+"/api/v1/transfers/t1/run", bearer(adminToken)); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET on an admin call: status %d, want 405", resp.StatusCode)
	}
}

func TestRunTransferNow(t *testing.T) {
	var buf bytes.Buffer
	var started, retried []string
	url := adminSite(t, []string{config.RunTransfer}, &started, &retried)
	audit.SetOutput(&buf)
	defer audit.SetOutput(io.Discard)

	for name, want := range map[string]int{
		"bank-to-bucket": http.StatusAccepted,
		"busy":           http.StatusConflict,
		"missing":        http.StatusNotFound,
	} {
		if resp, body := post(t, url+"/api/v1/transfers/"+name+"/run", bearer(adminToken)); resp.StatusCode != want {
			t.Errorf("%s: status %d (%s), want %d", name, resp.StatusCode, body, want)
		}
	}
	if len(started) != 1 || started[0] != "bank-to-bucket" {
		t.Fatalf("started %v", started)
	}
	if !strings.Contains(buf.String(), `"method":"run_transfer"`) || !strings.Contains(buf.String(), `"transfer":"bank-to-bucket"`) {
		t.Fatalf("admin calls must be audited: %s", buf.String())
	}

	// On a replica that doesn't run transfers, say so instead of pretending.
	h := config.HTTP{Admin: config.Admin{Tokens: []string{adminToken}, Allow: []string{config.RunTransfer}}}
	other := newSiteWith(t, h, nil).URL
	resp, body := post(t, other+"/api/v1/transfers/t1/run", bearer(adminToken))
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "SFTPGATE_TRANSFERS_ONLY_ON") {
		t.Fatalf("on another replica: status %d, %s", resp.StatusCode, body)
	}
}

func TestRetryActions(t *testing.T) {
	var started, retried []string
	url := adminSite(t, []string{config.RetryActions}, &started, &retried)

	resp, body := post(t, url+"/api/v1/actions/retry", bearer(adminToken))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"retried": 3`) {
		t.Fatalf("retry all: status %d, %s", resp.StatusCode, body)
	}
	if resp, _ := post(t, url+"/api/v1/actions/retry?job=j7", bearer(adminToken)); resp.StatusCode != http.StatusOK {
		t.Fatalf("retry one: status %d", resp.StatusCode)
	}
	if resp, _ := post(t, url+"/api/v1/actions/retry?job=gone", bearer(adminToken)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("retry unknown job: status %d, want 404", resp.StatusCode)
	}
	if strings.Join(retried, ",") != ",j7" {
		t.Fatalf("retried %q, want all then j7", retried)
	}
}
