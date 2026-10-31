---
title: Push to a partner
description: Send files from a bucket to a partner's SFTP server on a schedule.
---

Your service writes files to a bucket. SFTPgate sends them on a schedule, and your service needs no SFTP code at all.

Set up the [remote](../pull-from-a-partner/#1-set-up-the-remote) first, then add a transfer:

```yaml
transfers:
  - name: bank-payouts
    schedule: "*/30 * * * *"
    push:
      from: { storage: reports, prefix: payouts/outbox/, pattern: "*.csv" }
      to: { remote: bank, path: /in }
      after: "move:payouts/sent/"
```

Only files directly under the prefix are sent, not those in sub-folders. Each file is written under a temporary dot-name and renamed when complete, so the partner never sees half a file.

## After a send

| `after` | What happens to your copy |
|---|---|
| `keep` (default) | It stays. Files the partner already has are skipped. |
| `delete` | It's deleted from your bucket. |
| `move:<prefix>` | It moves, such as to a `sent` folder, so it's never sent twice. |

## Check and encrypt first

`before_push` can run `check` and `pgp_encrypt`. What's left is what gets sent.

```yaml
transfers:
  - name: bank-payouts
    # ...
    hooks:
      before_push: [settlement-csv, encrypt-for-bank]
      after_push: [notify-recon]
```

With `after: delete`, `after_push` can run only `webhook` actions, because the file is gone.

## Handle refused files

By default, a file that fails a check stays in the outbox and fails every run until someone fixes it. With `rejected`, it moves out, so it fails one run:

```yaml
push:
  from: { storage: reports, prefix: payouts/outbox/, pattern: "*.csv" }
  to: { remote: bank, path: /in }
  after: "move:payouts/sent/"
  rejected: "move:payouts/rejected/"
```

The move is audited as `denied`, with the reason. The prefix must differ from `from.prefix` and from the `after` prefix, and the transfer needs a `before_push` hook.

## Pause a transfer

Add `enabled: false` to the transfer. It's no longer scheduled, and the run-now call answers `409`.
