---
title: Built in and plug in
description: What is always on, what has a default, and what you add yourself.
---

With a minimal config, SFTPgate only moves files safely. Everything else is added by you, and costs nothing until you add it.

## You must provide

| What | Setting |
|---|---|
| Server identity | [`server.host_keys`](../../reference/server/#host_keys) |
| At least one bucket | [`storages`](../../reference/storages/) |
| Partners and their folders | [`accounts`](../../reference/accounts/), each with `folders` and `perms` |

## Always on

These can't be switched off.

| What | Notes |
|---|---|
| Complete files only | A file reaches the bucket only when it's finished |
| Integrity check | SHA-256 stored, and size and checksum read back after every save |
| Folder jail and permissions | A partner has only the `perms` you list |
| No overwrites | Unless `overwrite` is in `perms` |
| SFTP only | No shell, commands or tunnels |
| Modern encryption | No SHA-1 |
| Audit log | One JSON line per event, on the container's output |
| Secret check | `validate` refuses plain secrets in the config |
| Safe reload | A broken config is refused, and the old one keeps running |
| Graceful shutdown | Uploads in progress are waited for |
| `/healthz`, `/readyz`, `/metrics` | No token needed, so keep this port inside your network |

## On, with a default you can change

Leave one out and the default applies. You can't turn these off.

| What | Setting | Default |
|---|---|---|
| Connection limit | `server.max_connections` | 25 |
| Login tries per connection | `server.max_auth_tries` | 6 |
| Blocking IPs that keep failing | `server.throttle` | 5 failures in 10m blocks for 15m |
| Idle disconnect | `server.idle_timeout` | 15m |
| Free disk kept | `server.min_spool_free` | 1GiB |
| Action queue | `server.queue` | 4 workers, 8 tries, 30s backoff |
| Config check interval | `server.reload_interval` | 10s |
| Time zone | `server.timezone` | UTC |
| Login mode | `auth.mode` | `any`: a key or a password |
| After a push | `push.after` | `keep` |
| After a pull | `pull.delete_after` | `false` |

## Plug in

Off until you add it.

| What | How to add it | Without it |
|---|---|---|
| Webhook | [`webhook` action](../../guides/webhooks/), named in a hook | Nothing is notified |
| Copy | [`copy` action](../../guides/copy-files/), named in a hook | The file stays only where it landed |
| Checks | [`check` action](../../guides/check-files/), named in a hook | Any file is accepted |
| PGP | [`pgp_decrypt` or `pgp_encrypt`](../../guides/pgp/), named in a hook | Files are stored as they arrive |
| Scheduled pull or push | [`remotes` and `transfers`](../../reference/transfers/) | Nothing is fetched or sent on a schedule |
| Moving refused files | `push.rejected` | A refused file stays and fails every run |
| Read-only API | [`http.tokens`](../../reference/http/#tokens) | `/api/v1/` is off |
| Admin calls | [`http.admin`](../../reference/http/#admin) | Those calls answer 404 |
| Tracing | [`OTEL_EXPORTER_OTLP_ENDPOINT`](../../operate/tracing/) | No traces are sent |
| Server IP allowlist | `server.allow_ips` | Any IP may try to log in |
| Account IP allowlist | `accounts[].allow_ips` | Any IP the server allows |
| Two-step login | `auth.mode: all` | One of key or password is enough |
| Sessions per account | `max_sessions` | Limited only by `max_connections` |
| File name rules | `patterns` | Any name |
| File size limit | `max_file_size` | No limit; only the free disk check applies |
| Banner | `server.banner` | None |
| Config from git | [git-sync](../../deploy/config-from-git/) | You deliver the file yourself |
| Prometheus and Grafana on Kubernetes | [Helm `metrics.*`](../../reference/helm-values/#metrics) | Metrics are there, but nothing collects them |
| Ingress, autoscaling, PodDisruptionBudget | [Helm values](../../reference/helm-values/) | Not created |

An action does nothing just by being defined: a hook must name it. To pause one without deleting it, use `enabled: false`. The same setting pauses a transfer.

## What the Docker image contains

The image holds one program: the SFTP server, actions, transfers, API, `/metrics` and trace export. Prometheus, Grafana and Jaeger aren't inside it. They run separately as your own tools, and the repository provides a ready-made [dashboard and alert rules](../../operate/alerts/) for them.
