---
title: Copy files
description: Sort files into a data lake or archive automatically.
---

A `copy` action saves the current file to a storage, under a key you choose.

```yaml
actions:
  archive:
    copy:
      storage: warehouse
      key: "{account}/{date}/{filename}"

# in a folder or transfer
hooks:
  after_upload: [archive]
```

A file from `paygate` on 24 October lands at `paygate/2026-10-24/settlement.csv`.

## Key placeholders

| Placeholder | Becomes |
|---|---|
| `{account}` | The account name |
| `{folder}` | The folder |
| `{transfer}` | The transfer name |
| `{date}` | The date, in `server.timezone` |
| `{time}` | The time, in `server.timezone` |
| `{filename}` | The file's name |
| `{job_id}` | The job's id |

`{account}` and `{folder}` are empty for a file from a transfer, and `{transfer}` is empty for a file from an account. To use one action for both, write `"{account}{transfer}/{date}/{filename}"`.

## Copy after decrypting

Each action gets the file as the previous action left it, so a copy after `pgp_decrypt` saves the plain file. See [PGP](../pgp/).

Later webhooks list every copy in their `copies` field. See [webhooks](../webhooks/#what-the-receiver-gets).
