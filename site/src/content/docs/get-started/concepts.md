---
title: Core concepts
description: The ideas you need to read an SFTPgate config.
---

One config file describes everything. These are its building blocks. For any other term, see the [glossary](../../project/glossary/).

## Storage

A bucket with a short name. Everything else refers to it by that name. See [`storages`](../../reference/storages/).

```yaml
storages:
  recon-raw: "gs://acme-recon-raw"
```

## Account

One SFTP login, usually one per partner. It decides how the partner logs in, from which IPs, and which folders they see. See [`accounts`](../../reference/accounts/).

## Folder

A window onto one place in a bucket. The partner sees `/inbound`; the file really lands at `gs://acme-recon-raw/paygate/<name>`. The partner can never go above it, and can do only what `perms` lists. See [folders](../../reference/folders/).

```yaml
folders:
  - path: /inbound          # what the partner sees
    storage: recon-raw      # which bucket
    prefix: paygate/        # where inside it
    perms: [list, upload]   # what they may do
```

## Action

A named piece of work on a file, defined once. There are five kinds: `check`, `pgp_decrypt`, `pgp_encrypt`, `copy` and `webhook`. See [`actions`](../../reference/actions/).

## Hook

A point in a file's life where actions run, such as `before_upload` or `after_push`. A hook lists action names, which run in that order. An action does nothing until a hook names it. See [`hooks`](../../reference/hooks/).

```yaml
hooks:
  before_upload: [recon-csv]        # refuse a bad file
  after_upload: [archive, notify]   # in the background, with retries
```

## Remote and transfer

SFTPgate can also be the client. A **remote** is a partner's SFTP server. A **transfer** is a scheduled job that **pulls** from a remote into a bucket, or **pushes** from a bucket to a remote. See [`remotes`](../../reference/remotes/) and [`transfers`](../../reference/transfers/).

## Reference

Secrets never go in the config. A secret field holds a reference, such as `${env:NAME}`, that SFTPgate fills in at runtime. See [secrets](../../deploy/secrets/).

## Spool

A folder on local disk where an upload waits until it's complete. Then it goes to the bucket in one step, which is why half files can't appear there.

## Queue

Actions in `after_*` hooks become jobs on disk, so they survive a restart. A failed job is retried with growing waits. See [`server.queue`](../../reference/server/#queue).

## Hot reload

SFTPgate checks the config every 10 seconds. A valid change goes live without a restart, and only partners whose account changed are disconnected. An invalid file is refused, and the old config keeps running.
