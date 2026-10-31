---
title: Introduction
description: What SFTPgate is, who it is for, and how it works.
---

SFTPgate is an SFTP server for exchanging files with banks, vendors, partners and your own older systems. It runs as one container. Partners connect with any normal SFTP client, files go straight into your S3, GCS or Azure bucket, and every access is logged.

You configure it with one YAML file in git. Adding a partner is a pull request, and removing one is deleting those lines.

```
 Partners and services              SFTPgate                    Your cloud
 ─────────────────────      ──────────────────────────      ─────────────────
 Bank, payment gateway, SFTP  checks who, from where, what  saves  GCS / S3 / Azure
 tax provider, your    ─────▶ only their folders       ─────────▶  buckets
 older systems                complete files only              (all the data)
                                   │
                                   ├─▶ optional: check, decrypt, copy, notify
                                   ├─▶ optional: fetch from or send to partners on a schedule
                                   └─▶ audit log, metrics, alerts, traces, JSON API
```

SFTPgate is a bridge with a guard. The data always lives in your buckets. SFTPgate only holds a file for the few seconds it takes to arrive completely.

## What partners get

- A normal SFTP server. WinSCP, FileZilla, the `sftp` command and Paramiko all work, with nothing to install.
- Their own folders only, such as `/inbound` and `/outbound`. They can't see other partners.
- A stable server identity. The host key fingerprint you give them stays the same.

## What your team gets

- **Onboarding by pull request.** Every change has an author, a reviewer, a date and a reason.
- **Complete files only.** A file reaches the bucket after the partner finishes it. A dropped connection leaves nothing behind.
- **Proof of integrity.** Each file's SHA-256 is stored with it. After every save, SFTPgate reads the size and checksum back from the bucket before telling the partner it worked.
- **No accidental overwrites.** Without the `overwrite` permission, a file can never be replaced, even if two partners upload the same name at once.
- **Nothing extra to run.** There is no database to back up, migrate or patch.

## What security and compliance get

- An audit trail: one JSON line for every connection, login, file operation, action, transfer and config change.
- Layered access checks: server IP allowlist, connection limits, login throttling, modern encryption, account IP allowlist, key or password or both, and the folder jail.
- No secrets in git. The config holds references only, and `validate` refuses plain secrets.
- A hardened container: minimal image, non-root user, read-only filesystem.

## Where to go next

- [Run the quickstart](../quickstart/) to send your first file.
- [Learn the core concepts](../concepts/) if the terms are new.
- [See what's built in and what you add](../built-in-and-plug-in/).
