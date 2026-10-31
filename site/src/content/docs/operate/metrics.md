---
title: Metrics
description: Every Prometheus metric SFTPgate exposes.
---

SFTPgate exposes Prometheus metrics at `/metrics` on the HTTP port, with no token. To collect them, scrape the port, or on Kubernetes set `metrics.serviceMonitor.enabled: true`.

| Metric | Labels | What it counts |
|---|---|---|
| `sftpgate_connections_total` | `result` | TCP connections, accepted or rejected |
| `sftpgate_auth_total` | `account`, `method`, `result` | Login attempts |
| `sftpgate_sessions_active` | `account` | Open SFTP sessions |
| `sftpgate_operations_total` | `account`, `folder`, `op`, `result` | File operations |
| `sftpgate_transfer_bytes_total` | `account`, `folder`, `direction` | Bytes uploaded (`in`) and downloaded (`out`) |
| `sftpgate_transfer_duration_seconds` | `direction` | How long uploads and downloads take |
| `sftpgate_storage_errors_total` | `storage` | Errors returned by a storage |
| `sftpgate_throttled_ips` | | IPs currently blocked after too many failed logins |
| `sftpgate_spool_free_bytes` | | Free space on the spool disk |
| `sftpgate_config_reload_total` | `result` | Config reloads |
| `sftpgate_actions_total` | `step`, `result` | Action runs. `step="queue"` counts files that couldn't be queued. |
| `sftpgate_actions_queue_depth` | `state` | Jobs waiting (`pending`) or given up on (`failed`) |
| `sftpgate_transfer_runs_total` | `transfer`, `result` | Scheduled transfer runs |
| `sftpgate_transfer_files_total` | `transfer`, `result` | Files moved. `skipped` means it was already there; `denied` means a before hook refused it. |
| `sftpgate_config_info` | `hash`, `revision` | Always 1. The labels tell which config is running. |

## Dashboard

The repository includes a [Grafana dashboard](https://github.com/shashankpatil-dev/sftpgate/blob/main/deploy/helm/sftpgate/files/dashboard.json) with 19 panels: logins, connections, uploads and downloads per partner, data moved, transfer times, storage errors, denied requests, action jobs, scheduled transfers and the config version.

Install it with `metrics.grafanaDashboard.enabled: true`, or import the file.

## Health checks

| Path | Answers |
|---|---|
| `/healthz` | Is the process alive? Docker and Kubernetes restart it if not. |
| `/readyz` | Is it ready? It says no while a bucket can't be reached, so no partner is sent to a broken copy. |
