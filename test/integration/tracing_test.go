package integration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/shashankpatil-dev/sftpgate/internal/tracing"
)

// A traced upload must produce the expected span tree, link the post-upload
// job back to it, pass the trace on to the webhook, and never put file
// names into spans.
func TestTracing(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tracing.Use(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer tracing.Use(noop.NewTracerProvider())

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
	for _, name := range []string{"sftp.session", "sftp.upload", "storage.write", "sftp.download", "action.run", "action.webhook"} {
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

func names(spans []sdktrace.ReadOnlySpan) []string {
	var out []string
	for _, s := range spans {
		out = append(out, s.Name())
	}
	return out
}
