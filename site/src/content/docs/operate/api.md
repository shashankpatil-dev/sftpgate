---
title: API
description: The read-only JSON API and the two admin calls.
---

The API is for scripts and tools. It's off until you set [`http.tokens`](../../reference/http/#tokens). There's no web UI: use Grafana, your log system and traces for dashboards.

Send `Authorization: Bearer <token>`. A Basic login with the token as the password works too.

```sh
curl -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/status
```

## Read-only endpoints

| Endpoint | Returns |
|---|---|
| `GET /api/v1/status` | Version, uptime, config hash, readiness, and session and queue counts |
| `GET /api/v1/sessions` | Open sessions, with bytes moved |
| `GET /api/v1/events` | The newest audit events kept in memory, up to 2000 |
| `GET /api/v1/actions` | Action jobs waiting and failed |
| `GET /api/v1/config` | The config file as written, with secrets only as references |

`/events` takes the filters `event`, `account`, `result` and `limit`:

```sh
curl -H "Authorization: Bearer $TOKEN" \
  "localhost:8080/api/v1/events?account=paygate&result=denied&limit=50"
```

The events list starts empty after a restart. Full history is in your log system.

## Admin calls

Two calls change something. Each is off until you list it in [`http.admin.allow`](../../reference/http/#adminallow), and they take only admin tokens, never read-only ones. A call that isn't allowed answers `404`. Every call, allowed or refused, is an `admin` audit event. The config still changes only through git.

### Run a transfer now

```sh
curl -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  localhost:8080/api/v1/transfers/bank-statements/run
```

| Answer | Meaning |
|---|---|
| `202` | Started in the background. The result is a `transfer` audit event. |
| `404` | Unknown transfer, or `run_transfer` isn't allowed |
| `409` | Already running, paused with `enabled: false`, or transfers run on another copy |

The call must reach the pod named in `SFTPGATE_TRANSFERS_ONLY_ON`.

### Retry failed action jobs

```sh
curl -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  localhost:8080/api/v1/actions/retry                 # all of them
curl -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  "localhost:8080/api/v1/actions/retry?job=<id>"      # one
```

| Answer | Meaning |
|---|---|
| `200 {"retried": n}` | Moved back into the queue, with attempts reset |
| `404` | Unknown job, or `retry_actions` isn't allowed |

Failed jobs live on each pod's own disk, so call the pod that has them.

## Other endpoints

| Port | Path | What |
|---|---|---|
| 2222 | | SFTP |
| 8080 | `/metrics` | Prometheus metrics |
| 8080 | `/healthz` | The process is up |
| 8080 | `/readyz` | The config is loaded and every storage is reachable |
