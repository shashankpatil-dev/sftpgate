---
title: Environment variables
description: Variables SFTPgate reads when it starts.
---

| Variable | What it does |
|---|---|
| `SFTPGATE_CONFIG` | Path of the config file. Default `/etc/sftpgate/sftpgate.yaml`. |
| `SFTPGATE_REVISION` | The git commit to show in the API and metrics. Found automatically with git-sync. |
| `SFTPGATE_TRANSFERS` | `off` disables scheduled transfers on this copy. |
| `SFTPGATE_TRANSFERS_ONLY_ON` | Runs transfers only on the copy with this host name. Helm sets it. |
| `SFTPGATE_PPROF` | An address for Go's profiler, like `127.0.0.1:6060`, for finding memory or CPU problems. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Turns tracing on, like `http://otel-collector:4318`. |
| `OTEL_*` | Any other standard OpenTelemetry setting. |
| `AWS_*`, `GOOGLE_APPLICATION_CREDENTIALS`, `AZURE_STORAGE_*` | Cloud credentials, when not using workload identity. |
| Your own names | Values for `${env:NAME}` references. |
