// Package tracing sends OpenTelemetry traces when it is switched on.
//
// It is off unless OTEL_EXPORTER_OTLP_ENDPOINT or
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is set. Everything else uses the
// standard OTEL_* variables (headers, sampler, service name). When it is
// off, every span is a no-op that costs nothing.
//
// Spans never hold file names, object keys or file contents: tracing tools
// are often less locked down than the audit log. To find a file, look up
// the trace_id in the audit log.
package tracing

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Tracer creates all SFTPgate spans. It does nothing until Init or Use
// switches tracing on.
var Tracer trace.Tracer = noop.NewTracerProvider().Tracer("sftpgate")

// Use sends SFTPgate's spans to provider. Call it before serving.
//
// Only SFTPgate's own spans are traced. The global OpenTelemetry provider is
// left as a no-op on purpose: libraries like gocloud make their own spans,
// and those put object keys (file names) into error messages.
func Use(provider trace.TracerProvider) {
	Tracer = provider.Tracer("sftpgate")
}

// Init switches tracing on if an OTLP endpoint is configured. It returns a
// function that sends the last spans and stops tracing; call it on exit.
func Init(ctx context.Context, version string) (func(context.Context) error, error) {
	// W3C trace context, so webhook receivers can continue our traces.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName("sftpgate"),
		semconv.ServiceVersion(version),
	))
	if err != nil {
		return nil, err
	}

	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	Use(provider)
	return provider.Shutdown, nil
}

// Inject returns the W3C traceparent of the span in ctx, or "" if there is none.
func Inject(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// Extract returns the span context stored in a traceparent value.
func Extract(traceparent string) trace.SpanContext {
	carrier := propagation.MapCarrier{"traceparent": traceparent}
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), carrier)
	return trace.SpanContextFromContext(ctx)
}

// End finishes a span: "failed" if err is set, "ok" otherwise.
func End(span trace.Span, err error) {
	result := "ok"
	if err != nil {
		result = "failed"
	}
	Finish(span, result, err)
}

// TraceID returns the trace id of the span in ctx, or "" if it isn't traced.
func TraceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() || !sc.IsSampled() {
		return ""
	}
	return sc.TraceID().String()
}
