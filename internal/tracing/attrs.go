package tracing

import (
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gocloud.dev/gcerrors"
)

// Attribute names. Every span uses these same names, so a query or a span
// metric ("p95 of sftp.upload by sftpgate.account") works across all of them.
const (
	Account    = attribute.Key("sftpgate.account")
	Session    = attribute.Key("sftpgate.session")
	AuthMethod = attribute.Key("sftpgate.auth_method")
	Folder     = attribute.Key("sftpgate.folder")
	Storage    = attribute.Key("sftpgate.storage")
	Size       = attribute.Key("sftpgate.size")
	SHA256     = attribute.Key("sftpgate.sha256")
	Result     = attribute.Key("sftpgate.result")
	Transfer   = attribute.Key("sftpgate.transfer")
	Direction  = attribute.Key("sftpgate.direction")
	Remote     = attribute.Key("sftpgate.remote")
	Hook       = attribute.Key("sftpgate.hook")
	Job        = attribute.Key("sftpgate.job")
	Attempt    = attribute.Key("sftpgate.attempt")
	Offset     = attribute.Key("sftpgate.offset")
	Moved      = attribute.Key("sftpgate.moved")
	Skipped    = attribute.Key("sftpgate.skipped")
	Failed     = attribute.Key("sftpgate.failed")
)

// Finish ends a span with its result, one of the audit results: "ok",
// "denied" or "failed" (or "skipped" for a transfer file). Only "failed"
// marks the span as an error, so error rates show server or partner-system
// trouble, not partners being refused.
func Finish(span trace.Span, result string, err error) {
	span.SetAttributes(Result.String(result))
	switch {
	case result == "failed" && err != nil:
		msg := safeMessage(err)
		span.AddEvent("exception", trace.WithAttributes(attribute.String("exception.message", msg)))
		span.SetStatus(codes.Error, msg)
	case result == "failed":
		span.SetStatus(codes.Error, "failed")
	case err != nil:
		span.SetAttributes(attribute.String("sftpgate.reason", safeMessage(err)))
	}
	span.End()
}

// safeMessage is the error text for a span. Storage errors name the object
// key (the file name), so for those only their code is kept.
func safeMessage(err error) string {
	msg := err.Error()
	if code := gcerrors.Code(err); code != gcerrors.Unknown || strings.Contains(msg, `(key "`) {
		return "storage error (" + code.String() + ")"
	}
	return msg
}
