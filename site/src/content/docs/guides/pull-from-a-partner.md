---
title: Pull from a partner
description: Fetch files from a partner's SFTP server on a schedule.
---

Use this when the partner hosts the SFTP server and you fetch from it, such as "get the bank statements every morning at 6".

## 1. Set up the remote

Make a key for their server, and send them the **public** half:

```sh
ssh-keygen -t ed25519 -f bank_key
```

Get their host key, and confirm its fingerprint with them:

```sh
ssh-keyscan -t ed25519 sftp.bank.example
```

## 2. Add the remote and transfer

```yaml title="sftpgate.yaml"
version: 1

server:
  host_keys: [/etc/sftpgate/keys/ssh_host_ed25519_key]
  timezone: Asia/Kolkata

storages:
  recon-raw: "gs://acme-recon-raw"

remotes:
  bank:
    addr: sftp.bank.example:22
    user: acme
    host_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
    private_key: "${file:/etc/sftpgate/secrets/bank_key}"

transfers:
  - name: bank-statements
    schedule: "0 6 * * *"
    pull:
      from: { remote: bank, path: /out, pattern: "*.csv" }
      to: { storage: recon-raw, key: "bank/{date}/{filename}" }
      delete_after: false
```

The schedule uses `server.timezone`. If the server ever shows another host key, SFTPgate refuses to connect.

## How a run works

- Files already in the bucket are skipped.
- With `delete_after`, a remote file is removed once it's stored. A skipped file is never deleted: SFTPgate only removes files it stored itself in that run.
- One bad file doesn't stop the others, but the run is marked failed.
- A run is skipped if the previous one is still going.

## Check and react

```yaml
transfers:
  - name: bank-statements
    # ...
    hooks:
      before_pull: [settlement-csv]   # a bad file isn't stored
      after_pull: [notify-recon]      # in the background
```

## Test it now

With the [admin call](../../operate/api/#run-a-transfer-now) switched on:

```sh
curl -X POST -H "Authorization: Bearer $ADMIN_TOKEN" \
  localhost:8080/api/v1/transfers/bank-statements/run
```

The result appears as a `transfer` event in the audit log.
