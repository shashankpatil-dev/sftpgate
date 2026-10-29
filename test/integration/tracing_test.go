package integration

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
	"gocloud.dev/blob/memblob"
	"golang.org/x/crypto/ssh"

	"github.com/shashankpatil-dev/sftpgate/internal/tracing"
)

// A traced upload must produce the expected span tree, link the post-upload
// job back to it, pass the trace on to the webhook, and never put file
// names into spans.
func TestTracing(t *testing.T) {
	rec := recordSpans(t)

	// A library tracing into the global provider must not reach us.
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	defer otel.SetTracerProvider(noop.NewTracerProvider())

	traceparent := make(chan string, 1)
	hook := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		traceparent <- r.Header.Get("traceparent")
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
	sftpRoundTrip(t, c)

	var header string
	select {
	case header = <-traceparent:
	case <-time.After(5 * time.Second):
		t.Fatal("webhook was not called")
	}
	_ = c.Close()
	auditLine := h.audit.waitFor(t, `"event":"session_end"`)

	spans := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		spans[s.Name()] = s
	}
	for _, name := range []string{"sftp.session", "sftp.login", "sftp.upload", "storage.write", "sftp.download", "storage.read", "action.run", "action.webhook"} {
		if spans[name] == nil {
			t.Fatalf("span %s missing; got %v", name, names(rec.Ended()))
		}
	}

	session, upload, run := spans["sftp.session"], spans["sftp.upload"], spans["action.run"]
	if upload.Parent().SpanID() != session.SpanContext().SpanID() {
		t.Error("sftp.upload must be a child of sftp.session")
	}
	if spans["storage.write"].Parent().SpanID() != upload.SpanContext().SpanID() {
		t.Error("storage.write must be a child of sftp.upload")
	}
	if spans["sftp.login"].Parent().SpanID() != session.SpanContext().SpanID() {
		t.Error("sftp.login must be a child of sftp.session")
	}
	if spans["storage.read"].Parent().SpanID() != spans["sftp.download"].SpanContext().SpanID() {
		t.Error("storage.read must be a child of sftp.download")
	}
	// The same attribute names everywhere, so span metrics can group by them.
	for name, want := range map[string]map[string]string{
		"sftp.login":    {"sftpgate.account": "keyuser", "sftpgate.auth_method": "publickey", "sftpgate.result": "ok"},
		"sftp.session":  {"sftpgate.account": "keyuser", "sftpgate.result": "ok"},
		"sftp.upload":   {"sftpgate.account": "keyuser", "sftpgate.folder": "/data", "sftpgate.result": "ok", "sftpgate.size": "5"},
		"sftp.download": {"sftpgate.account": "keyuser", "sftpgate.result": "ok"},
		"action.run":    {"sftpgate.account": "keyuser", "sftpgate.hook": "after_upload", "sftpgate.result": "ok"},
	} {
		attrs := attrMap(spans[name])
		for k, v := range want {
			if attrs[k] != v {
				t.Errorf("%s: %s = %q, want %q", name, k, attrs[k], v)
			}
		}
	}
	if len(attrMap(upload)["sftpgate.sha256"]) != 64 {
		t.Error("sftp.upload must carry the file's sha256")
	}
	if len(run.Links()) != 1 || run.Links()[0].SpanContext.SpanID() != upload.SpanContext().SpanID() {
		t.Error("action.run must link back to the upload span")
	}
	if run.SpanContext().TraceID() == upload.SpanContext().TraceID() {
		t.Error("action.run must start its own trace")
	}
	if !strings.Contains(header, run.SpanContext().TraceID().String()) {
		t.Errorf("webhook traceparent %q must carry the action's trace", header)
	}
	if !strings.Contains(auditLine, `"trace_id":"`+session.SpanContext().TraceID().String()+`"`) {
		t.Errorf("audit line must carry the session trace id: %s", auditLine)
	}

	for _, s := range rec.Ended() {
		for _, a := range s.Attributes() {
			if v := a.Value.String(); strings.Contains(v, "hello.txt") || strings.Contains(v, "key/") {
				t.Errorf("span %s leaks a file name or key in %s=%s", s.Name(), a.Key, v)
			}
		}
	}
}

// A wrong password is traced as a denied login, not as an error; a port
// check that leaves without a word is not traced at all.
func TestTracingLogins(t *testing.T) {
	rec := recordSpans(t)
	h := newHarness(t, nil)

	if c, err := h.dial("keyuser", ssh.Password("wrong")); err == nil {
		_ = c.Close()
		t.Fatal("wrong password accepted")
	}
	h.audit.waitFor(t, `"event":"auth_failed"`)
	probe, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = probe.Close()
	time.Sleep(200 * time.Millisecond)

	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("want sftp.login and sftp.session for the failed login only, got %v", names(spans))
	}
	for _, s := range spans {
		if attrMap(s)["sftpgate.result"] != "denied" || s.Status().Code == codes.Error {
			t.Errorf("%s: result %q, status %v; want denied and no error status", s.Name(), attrMap(s)["sftpgate.result"], s.Status().Code)
		}
	}
}

// An upload refused by a before_upload check is "denied", with the check's
// reason, and is not an error.
func TestTracingRejectedUpload(t *testing.T) {
	rec := recordSpans(t)
	h := newHarness(t, withHooks("http://127.0.0.1:1/unused"))
	c, err := h.dial("keyuser", h.withKey())
	if err != nil {
		t.Fatal(err)
	}
	sc, err := sftp.NewClient(c)
	if err != nil {
		t.Fatal(err)
	}
	_ = sftpPut(sc, "/data/bad.csv", "name,total\nx,1\n")
	_ = sc.Close()
	_ = c.Close()
	h.audit.waitFor(t, `"event":"session_end"`)

	for _, s := range rec.Ended() {
		if s.Name() != "sftp.upload" && s.Name() != "action.check" {
			continue
		}
		a := attrMap(s)
		if a["sftpgate.result"] != "denied" || s.Status().Code == codes.Error || !strings.Contains(a["sftpgate.reason"], "CSV header") {
			t.Errorf("%s: attrs %v, status %v; want denied with the reason", s.Name(), a, s.Status().Code)
		}
	}
}

// Each transfer run is a trace: transfer.run, transfer.connect, then one
// transfer.file per file holding its before hook. The after_push job links
// back to the file.
func TestTracingTransfers(t *testing.T) {
	rec := recordSpans(t)
	e := newHookedEnv(t)
	put(t, e.mine, "outbox/good.csv", "id,amount\n1,10\n")
	put(t, e.mine, "outbox/bad.csv", "name\nx\n")
	_ = e.sched.RunNow(context.Background(), "push-and-move")
	e.webhooks()

	byName := map[string][]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		byName[s.Name()] = append(byName[s.Name()], s)
	}
	if len(byName["transfer.run"]) != 1 || len(byName["transfer.connect"]) != 1 || len(byName["transfer.file"]) != 2 {
		t.Fatalf("spans: %v", names(rec.Ended()))
	}
	run := byName["transfer.run"][0]
	if a := attrMap(run); a["sftpgate.transfer"] != "push-and-move" || a["sftpgate.direction"] != "push" ||
		a["sftpgate.remote"] != "partner" || a["sftpgate.moved"] != "1" || a["sftpgate.failed"] != "1" || run.Status().Code != codes.Error {
		t.Errorf("transfer.run attrs %v, status %v", a, run.Status().Code)
	}
	results := map[string]bool{}
	var good sdktrace.ReadOnlySpan
	for _, f := range byName["transfer.file"] {
		if f.Parent().SpanID() != run.SpanContext().SpanID() {
			t.Error("transfer.file must be a child of transfer.run")
		}
		results[attrMap(f)["sftpgate.result"]] = true
		if attrMap(f)["sftpgate.result"] == "ok" {
			good = f
		}
	}
	if !results["ok"] || !results["denied"] {
		t.Fatalf("want one ok and one denied file, got %v", results)
	}
	for _, s := range rec.Ended() {
		if s.Name() == "action.pgp_encrypt" && s.Parent().SpanID() != good.SpanContext().SpanID() {
			t.Error("before_push actions must be children of their transfer.file")
		}
	}
	jobs := byName["action.run"]
	if len(jobs) != 1 || len(jobs[0].Links()) != 1 || jobs[0].Links()[0].SpanContext.SpanID() != good.SpanContext().SpanID() {
		t.Error("the after_push job must link back to its transfer.file")
	}
}

// recordSpans sends SFTPgate's spans to a recorder until the test ends.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tracing.Use(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { tracing.Use(noop.NewTracerProvider()) })
	return rec
}

func attrMap(s sdktrace.ReadOnlySpan) map[string]string {
	m := map[string]string{}
	for _, a := range s.Attributes() {
		m[string(a.Key)] = a.Value.String()
	}
	return m
}

func names(spans []sdktrace.ReadOnlySpan) []string {
	var out []string
	for _, s := range spans {
		out = append(out, s.Name())
	}
	return out
}

// Storage errors name the object key; a failed span keeps only their code.
func TestSpanErrorsHideKeys(t *testing.T) {
	rec := recordSpans(t)
	b := memblob.OpenBucket(nil)
	defer b.Close()
	_, err := b.ReadAll(context.Background(), "secret/report-2026.csv")
	if err == nil || !strings.Contains(err.Error(), "report-2026.csv") {
		t.Fatalf("expected a not-found error naming the key, got %v", err)
	}
	_, span := tracing.Tracer.Start(context.Background(), "x")
	tracing.End(span, err)

	s := rec.Ended()[0]
	if s.Status().Description != "storage error (NotFound)" {
		t.Errorf("status = %q", s.Status().Description)
	}
	for _, ev := range s.Events() {
		for _, a := range ev.Attributes {
			if strings.Contains(a.Value.String(), "report-2026") {
				t.Errorf("span event leaks the key: %s", a.Value.String())
			}
		}
	}
}
