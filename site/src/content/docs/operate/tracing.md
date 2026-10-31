---
title: Tracing
description: See each login, upload and transfer as a timeline.
---

Set `OTEL_EXPORTER_OTLP_ENDPOINT`, such as `http://otel-collector:4318`, to send OpenTelemetry traces over OTLP/HTTP. All the other standard `OTEL_*` variables work too. Without it, tracing is off and costs nothing.

Tracing can't break the real work. Spans are sent in the background, so if the collector is down, files still move.

## What is traced

| Trace | Spans |
|---|---|
| One SFTP connection | `sftp.session`, with `sftp.login`, then a span per upload, download and file command, with `storage.read` and `storage.write` under them |
| One transfer run | `transfer.run`, `transfer.connect`, and a `transfer.file` per file |
| One action job | `action.run`, linked to the upload or transfer file that caused it |

Health probes aren't traced. Audit lines include `trace_id`, and webhooks carry a `traceparent` header, so the receiver can continue the trace.

## Attributes

Every span uses the same names, such as `sftpgate.account`, `.folder`, `.transfer`, `.size` and `.sha256`.

`sftpgate.result` is `ok`, `denied` or `failed`, as in the audit log. Only `failed` marks a span as an error, so span metrics in Tempo, Jaeger or Datadog can alert on real trouble without counting refused files. For example:

- p95 of `sftp.upload` for one partner
- error rate of `transfer.run`

## Privacy

Spans never contain file names or object keys, because tracing tools are often less locked down than audit logs. Storage errors keep only their code.
