---
title: Check files
description: Refuse a broken report at the door, before it breaks something later.
---

A `check` action refuses a file that doesn't look right. In `before_upload` it runs while the partner waits, so a bad file is never stored.

## Refuse a bad CSV

```yaml title="sftpgate.yaml"
version: 1

server:
  host_keys: [/etc/sftpgate/keys/ssh_host_ed25519_key]

storages:
  main: "gs://acme-recon-raw"

actions:
  settlement-csv:
    check:
      min_size: 100
      max_size: 200MiB
      csv:
        header: [settlement_id, payment_id, amount]
        min_rows: 1

accounts:
  - name: paygate
    auth:
      public_keys: ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl paygate"]
    folders:
      - path: /inbound
        storage: main
        prefix: paygate/
        perms: [list, upload]
        hooks:
          before_upload: [settlement-csv]
```

With `csv`, every row must also have as many columns as the first one. All the rules are in the [`check` reference](../../reference/actions/#check).

## Check an encrypted file

An encrypted file can't be read before it's decrypted, so run the check after decrypting, in `after_upload`:

```yaml
hooks:
  after_upload: [decrypt-paygate, settlement-csv]
```

A failed check in an `after_*` hook gives up at once, because retrying can't fix the file. The actions after it don't run, and the `SftpgateActionsGaveUp` alert fires.

## Check before sending or storing

| Hook | If the check fails |
|---|---|
| `before_upload` | The upload is refused, and nothing is stored |
| `before_pull` | The file isn't stored or deleted on their side, and the run fails |
| `before_push` | The file isn't sent, and the run fails. See [`rejected`](../push-to-a-partner/#handle-refused-files) |

## What a partner sees

SFTP can only refuse a file when the client closes it, and most clients don't show the server's message then.

| Client | Shows |
|---|---|
| Go `pkg/sftp` | The reason, such as `upload rejected: CSV header is ["name"], expected ["id" "amount"]` |
| OpenSSH `sftp` | `Failure` |
| PuTTY `psftp`, the engine of WinSCP and FileZilla | `error while closing: failure` |
| Paramiko | Ignores errors on close. With its default `confirm=True`, the upload then fails with "no such file" |

The file is never stored either way, and the audit log always has the reason (`event: upload`, `result: denied`). Tell partners that a refused upload means "fix the file and send it again", and ask Paramiko users to keep `confirm=True`.

## Pause a check

If a partner needs time to fix their export, add `enabled: false` to the action. It stays in the file, and every hook skips it.
