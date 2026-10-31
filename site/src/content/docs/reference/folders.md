---
title: folders
description: What each account sees, and what it may do there.
---

Each item in `accounts[].folders` is a window onto one place in a bucket. The partner can never go above it.

```yaml
folders:
  - path: /inbound
    storage: recon-raw
    prefix: paygate/
    perms: [list, upload]
    patterns: ["*.csv", "*.csv.pgp"]
    max_file_size: 500MiB
    hooks:
      before_upload: [settlement-csv]
      after_upload: [archive, notify]
```

## `path`

Text. Required.

The folder name the partner sees, one level deep, like `/inbound`.

## `storage`

Name. Required.

Which [storage](../storages/) the files live in.

## `prefix`

Text. Default empty.

The place inside the bucket, like `paygate/inbound/`. End it with `/`.

## `perms`

List. Required.

What the partner may do here.

| Permission | Allows |
|---|---|
| `list` | Seeing the files |
| `download` | Fetching files |
| `upload` | Sending new files |
| `overwrite` | Replacing a file that already exists. Otherwise it's refused. |
| `delete` | Removing files |
| `rename` | Renaming or moving files inside this folder |
| `mkdir` | Creating sub-folders, and removing empty ones |

Without `overwrite`, a file can never be replaced, even when two partners upload the same name at the same moment. The bucket itself refuses the second save, and that partner gets "file already exists".

## `patterns`

List. Default empty, which allows any name.

The file names that may be uploaded. `*` matches anything.

## `max_file_size`

Size. Default `0`, which means no limit.

The largest file allowed. It's also the disk space reserved for each upload, so keep it close to real file sizes. See [sizing](../../operate/sizing/).

## `hooks`

Map. Default empty.

Actions to run, by name: `before_upload`, `after_upload` and `after_download`. See [hooks](../hooks/).

## `on_upload`

List. Default empty.

The older way to write `hooks.after_upload`, with the steps in place. It still works, but not together with `hooks.after_upload`.
