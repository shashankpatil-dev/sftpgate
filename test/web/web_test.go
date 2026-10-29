package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shashankpatil-dev/sftpgate/internal/actions"
	"github.com/shashankpatil-dev/sftpgate/internal/audit"
	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/server"
	"github.com/shashankpatil-dev/sftpgate/internal/web"
)

const token = "a-long-enough-test-token"

func newSite(t *testing.T, tokens []string) *httptest.Server {
	return newSiteWith(t, config.HTTP{Tokens: tokens}, nil)
}

// newSiteWith builds a site with these HTTP settings; change, if set, can
// replace parts of the source.
func newSiteWith(t *testing.T, h config.HTTP, change func(*web.Source)) *httptest.Server {
	t.Helper()
	audit.SetOutput(io.Discard)
	cfg := &config.Config{
		HTTP: h,
		Raw:  []byte("auth:\n  password_hash: ${env:PW_HASH}\n"),
		Hash: "0123456789abcdef0123",
	}
	src := web.Source{
		Version: "test",
		Started: time.Now().Add(-time.Hour),
		Config:  func() *config.Config { return cfg },
		Ready:   func(context.Context) error { return nil },
		Sessions: func() []server.SessionInfo {
			return []server.SessionInfo{{ID: "s1", Account: "web-vendor", IP: "203.0.113.7", Method: "publickey", Started: time.Now(), BytesIn: 2048}}
		},
		Actions: func() ([]actions.Job, []actions.Job, error) {
			return []actions.Job{{ID: "j1", Account: "web-vendor", Filename: "a.csv", Attempt: 1}}, nil, nil
		},
	}
	if change != nil {
		change(&src)
	}
	srv := httptest.NewServer(web.Handler(src))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string, auth func(*http.Request)) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
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

func basic(tok string) func(*http.Request) {
	return func(r *http.Request) { r.SetBasicAuth("anyone", tok) }
}

func bearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func TestAPIIsOffWithoutTokens(t *testing.T) {
	site := newSite(t, nil)
	for _, path := range []string{"/api/v1/status", "/api/v1/events"} {
		if resp, _ := get(t, site.URL+path, basic(token)); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404 when no tokens are set", path, resp.StatusCode)
		}
	}
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		if resp, _ := get(t, site.URL+path, nil); resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d, want 200 without any token", path, resp.StatusCode)
		}
	}
}

func TestTokens(t *testing.T) {
	site := newSite(t, []string{token})
	cases := []struct {
		name string
		auth func(*http.Request)
		want int
	}{
		{"no token", nil, http.StatusUnauthorized},
		{"wrong token", basic("wrong-token-of-some-length"), http.StatusUnauthorized},
		{"empty password", basic(""), http.StatusUnauthorized},
		{"basic login", basic(token), http.StatusOK},
		{"bearer token", bearer(token), http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := get(t, site.URL+"/api/v1/status", tc.auth)
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.want)
			}
			if tc.want == http.StatusUnauthorized && !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Bearer") {
				t.Fatal("a 401 must say a Bearer token is needed")
			}
			if resp.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("API answers must not be cached")
			}
		})
	}
}

func TestOnlyGET(t *testing.T) {
	site := newSite(t, []string{token})
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req, _ := http.NewRequest(method, site.URL+"/api/v1/status", strings.NewReader("{}"))
		req.SetBasicAuth("x", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 405", method, resp.StatusCode)
		}
	}
}

func TestStatusSessionsActionsAndConfig(t *testing.T) {
	site := newSite(t, []string{token})

	_, body := get(t, site.URL+"/api/v1/status", bearer(token))
	var st web.Status
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Ready || st.Sessions != 1 || st.ActionsPending != 1 || st.Version != "test" || st.UptimeSeconds < 3500 {
		t.Fatalf("status = %+v", st)
	}

	_, body = get(t, site.URL+"/api/v1/sessions", bearer(token))
	if !strings.Contains(body, `"account": "web-vendor"`) || !strings.Contains(body, `"bytes_in": 2048`) {
		t.Fatalf("sessions = %s", body)
	}

	_, body = get(t, site.URL+"/api/v1/actions", bearer(token))
	if !strings.Contains(body, `"id": "j1"`) {
		t.Fatalf("actions = %s", body)
	}

	resp, body := get(t, site.URL+"/api/v1/config", bearer(token))
	if !strings.Contains(body, "${env:PW_HASH}") || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("config = %q", body)
	}
}

func TestEventFilters(t *testing.T) {
	site := newSite(t, []string{token})
	audit.Emit(audit.Event{Event: "upload", Result: audit.OK, Account: "filter-a"})
	audit.Emit(audit.Event{Event: "upload", Result: audit.Denied, Account: "filter-a"})
	audit.Emit(audit.Event{Event: "download", Result: audit.OK, Account: "filter-b"})

	var events []audit.Event
	_, body := get(t, site.URL+"/api/v1/events?account=filter-a&result=denied", bearer(token))
	if err := json.Unmarshal([]byte(body), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Result != audit.Denied || events[0].Time.IsZero() {
		t.Fatalf("filtered events = %+v", events)
	}

	_, body = get(t, site.URL+"/api/v1/events?account=filter-a&limit=1", bearer(token))
	_ = json.Unmarshal([]byte(body), &events)
	if len(events) != 1 || events[0].Result != audit.Denied {
		t.Fatalf("limit=1 must return only the newest event, got %+v", events)
	}
}

func TestNotReadyIsShown(t *testing.T) {
	audit.SetOutput(io.Discard)
	cfg := &config.Config{HTTP: config.HTTP{Tokens: []string{token}}}
	site := httptest.NewServer(web.Handler(web.Source{
		Config:   func() *config.Config { return cfg },
		Ready:    func(context.Context) error { return errors.New("storage raw: unreachable") },
		Sessions: func() []server.SessionInfo { return nil },
		Actions:  func() ([]actions.Job, []actions.Job, error) { return nil, nil, errors.New("queue unreadable") },
	}))
	defer site.Close()

	resp, _ := get(t, site.URL+"/readyz", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status %d, want 503", resp.StatusCode)
	}
	_, body := get(t, site.URL+"/api/v1/status", bearer(token))
	if !strings.Contains(body, "storage raw: unreachable") {
		t.Fatalf("the status must say why it is not ready: %s", body)
	}
	if resp, _ := get(t, site.URL+"/api/v1/actions", bearer(token)); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("an unreadable queue must be a 500, got %d", resp.StatusCode)
	}
	if resp, _ := get(t, site.URL+"/ui/", basic(token)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the HTML UI is gone, /ui/ must be a 404, got %d", resp.StatusCode)
	}
}
