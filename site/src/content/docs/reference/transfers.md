---
title: transfers
description: Scheduled jobs that move files between a remote and a bucket.
---

With several pods, transfers run on one pod only, so each file moves once. See the guides to [pull](../../guides/pull-from-a-partner/) and [push](../../guides/push-to-a-partner/).

```yaml
transfers:
  - name: bank-statements
    schedule: "0 6 * * *"
    pull:
      from: { remote: bank, path: /out, pattern: "*.csv" }
      to: { storage: recon-raw, key: "bank/{date}/{filename}" }
  - name: bank-payouts
    schedule: "*/30 * * * *"
    push:
      from: { storage: reports, prefix: payouts/outbox/, pattern: "*.csv" }
      to: { remote: bank, path: /in }
      after: "move:payouts/sent/"
```

## Every transfer

### `name`

Text. Required.

A unique name, lowercase with `-`. It appears in logs, metrics and the API.

### `schedule`

Cron. Required.

When to run, in `server.timezone`.

| Schedule | Meaning |
|---|---|
| `0 6 * * *` | Daily at 06:00 |
| `0 10,18 * * *` | 10:00 and 18:00 daily |
| `*/30 * * * *` | Every 30 minutes |
| `0 9 * * 1-5` | 09:00, Monday to Friday |
| `@every 10m` | Every 10 minutes |

### `enabled`

Yes or no. Default on.

`false` pauses the transfer: it isn't scheduled, and the run-now call answers `409`.

### `hooks`

Map. Pull transfers take `before_pull` and `after_pull`; push transfers take `before_push` and `after_push`. See [hooks](../hooks/).

## pull

Fetches from the partner into a bucket. A transfer has `pull` or `push`, not both.

### `pull.from.remote`

Name. Required. Which [remote](../remotes/).

### `pull.from.path`

Text. Required. The folder on their server, like `/out`.

### `pull.from.pattern`

Text. Default empty, which means all files. Which files, like `*.csv`.

### `pull.to.storage`

Name. Required. The target storage.

### `pull.to.key`

Text. Required. The target name, which must contain `{filename}`. Placeholders: `{name}`, `{date}`, `{time}`, `{filename}`.

### `pull.delete_after`

Yes or no. Default `false`.

`true` removes their copy once it's stored.

## push

Sends from a bucket to the partner.

### `push.from.storage`

Name. Required. The source storage.

### `push.from.prefix`

Text. Files directly under this prefix, like `payouts/outbox/`. Sub-folders aren't included.

### `push.from.pattern`

Text. Default empty, which means all files.

### `push.to.remote`

Name. Required. Which remote.

### `push.to.path`

Text. Required. The folder on their server, like `/in`.

### `push.after`

Default `keep`.

| Value | What happens to your copy after a send |
|---|---|
| `keep` | It stays. Files they already have are skipped. |
| `delete` | It's deleted from your bucket. |
| `move:<prefix>` | It moves there, so it's never sent twice. |

### `push.rejected`

`move:<prefix>`. Optional.

A file refused by a `before_push` check moves here, so it fails one run, not every run. It must differ from `from.prefix` and the `after` prefix, and the transfer needs `before_push`.
