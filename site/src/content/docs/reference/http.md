---
title: http
description: Health checks, metrics, the read-only API and the admin calls.
---

```yaml
http:
  listen: ":8080"
  tokens: ["${env:SFTPGATE_API_TOKEN}"]
  admin:
    tokens: ["${env:SFTPGATE_ADMIN_TOKEN}"]
    allow: [run_transfer, retry_actions]
```

:::caution
Keep this port inside your network. `/metrics`, `/healthz` and `/readyz` need no token.
:::

## `listen`

Address. Default `":8080"`.

The address for `/metrics`, `/healthz`, `/readyz` and `/api/v1/`.

## `tokens`

List of references. Default empty, which keeps the API off.

Tokens that open the [read-only API](../../operate/api/). Each must be at least 16 characters. Several are allowed, so you can change one without cutting everyone off.

## `admin`

The only API calls that change something. They're off unless both settings below are set.

### `admin.tokens`

List of references. Default empty.

Tokens for the admin calls, at least 16 characters, and different from every token in `tokens`. They work only as a Bearer header, so a browser can't be tricked into sending one.

### `admin.allow`

List. Default empty.

The admin calls that exist. Anything not listed answers `404`, as if it didn't exist.

| Value | Call |
|---|---|
| `run_transfer` | [`POST /api/v1/transfers/<name>/run`](../../operate/api/#run-a-transfer-now) |
| `retry_actions` | [`POST /api/v1/actions/retry`](../../operate/api/#retry-failed-action-jobs) |
