---
title: hooks
description: When actions run, what each hook may run, and what a failure does.
---

Hooks are set on [folders](../folders/) and [transfers](../transfers/). Each lists action names, which run in that order.

```yaml
hooks:
  before_upload: [recon-csv]
  after_upload: [warehouse, notify]
```

"Before" hooks only check (or, for a push, encrypt), because something is waiting on them. "After" hooks run in the background with retries.

| Hook | On | Runs |
|---|---|---|
| [`before_upload`](#before_upload) | Folder | `check` |
| [`after_upload`](#after_upload) | Folder | Any action |
| [`after_download`](#after_download) | Folder | Any action |
| [`before_push`](#before_push) | Push transfer | `check`, `pgp_encrypt` |
| [`after_push`](#after_push) | Push transfer | Any action |
| [`before_pull`](#before_pull) | Pull transfer | `check` |
| [`after_pull`](#after_pull) | Pull transfer | Any action |

## Folder hooks

### `before_upload`

Runs while the partner waits. If a check fails, the upload is refused and nothing is stored. The reason goes to the partner's client and the audit log (`result: denied`).

### `after_upload`

Runs in the background. A failed action is retried. After `max_attempts`, the job moves to `queue/failed`.

### `after_download`

Runs once the partner has the whole file. A failed action is retried.

## Push hooks

### `before_push`

What's left after these actions is what gets sent. If one fails, the file isn't sent and the run fails. It stays in place, or moves to the [`rejected`](../transfers/#pushrejected) prefix if set.

### `after_push`

A failed action is retried. With `after: delete`, only `webhook` actions may run, since the file is gone.

## Pull hooks

### `before_pull`

If a check fails, the file isn't stored, isn't deleted on their side, and the run fails.

### `after_pull`

A failed action is retried.

## When actions fail

A failed `check` in an `after_*` hook gives up at once, because retrying can't fix the file. The actions after it don't run.

Other actions wait in a queue on disk, so they survive restarts. They're retried with growing waits (30s, 1m, 2m and so on, up to 1h). See [`server.queue`](../server/#queue). You can also [retry failed jobs](../../operate/api/#retry-failed-action-jobs) with an admin call.

A job can run more than once, so webhook receivers should ignore a `job_id` they've already seen.
