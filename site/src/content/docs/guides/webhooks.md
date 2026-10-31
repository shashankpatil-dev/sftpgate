---
title: Webhooks
description: Tell your systems the moment a file is uploaded, downloaded, pushed or pulled.
---

A `webhook` action sends a signed HTTP POST describing the file.

```yaml
actions:
  notify-recon:
    webhook:
      url: "https://recon.internal.acme.in/hooks/sftpgate"
      secret: "${env:RECON_WEBHOOK_SECRET}"

# in a folder
hooks:
  after_upload: [notify-recon]
  after_download: [notify-recon]
```

Webhooks are for file events. To hear about failures, such as a transfer that failed, use [alerts](../alerts-to-slack/).

## What the receiver gets

```json
{
  "event": "upload",
  "job_id": "20261024T075812Z-1a2b3c4d",
  "account": "paygate",
  "folder": "/inbound",
  "storage": "recon-raw",
  "key": "paygate/inbound/settlement.csv.pgp",
  "filename": "settlement.csv.pgp",
  "size": 10482,
  "sha256": "ab12...",
  "time": "2026-10-24T07:58:12Z",
  "uploaded": "2026-10-24T07:58:12Z",
  "copies": [{ "storage": "warehouse", "key": "paygate/2026-10-24/settlement.csv" }]
}
```

- `event` is `upload`, `download`, `push` or `pull`.
- For transfers, `transfer`, `remote` and `remote_path` are set, and `account` and `folder` are empty.
- For a push, `key` is where the file is now, such as in `sent/`, and `remote_path` is the name the partner got.
- `uploaded` holds the same time as `time`, for receivers written for v0.3.
- `sha256` can be empty for a pushed file that no action read.

## What the receiver must do

1. **Check the signature.** Compute HMAC-SHA256 of the raw body with the secret, and compare it with the `X-Sftpgate-Signature: sha256=<hex>` header.
2. **Ignore repeats.** A retry can send the same `job_id` twice.
3. **Answer quickly.** Any 2xx within 10 seconds counts as done. Anything else is retried.

```python title="receiver.py"
import hashlib, hmac

def valid(body: bytes, header: str, secret: bytes) -> bool:
    expected = "sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, header)
```

## Add your receiver's own auth

```yaml
webhook:
  url: "https://recon.internal.acme.in/hooks/sftpgate"
  headers:
    Authorization: "${env:RECON_API_TOKEN}"
```

Headers whose name contains auth, token, key, secret or cookie must be references. `Content-Type`, `User-Agent` and `X-Sftpgate-*` can't be set.

## Retries

Webhooks run in the background. A failed job is retried with growing waits (30s, 1m, 2m and so on, up to 1h). After `server.queue.max_attempts`, it moves to `queue/failed` and the `SftpgateActionsGaveUp` alert fires. You can [retry failed jobs](../../operate/api/#retry-failed-action-jobs) with an admin call.

A hook with only webhooks doesn't download the file. It checks the SHA-256 stored with it instead, so a replaced file is never announced.

Webhooks carry a `traceparent` header, so the receiver can [continue the trace](../../operate/tracing/).
