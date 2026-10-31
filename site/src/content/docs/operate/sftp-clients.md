---
title: SFTP clients
description: Which SFTP clients work, and how they log in.
---

Each test uploads, lists, downloads and compares checksums.

| Client | Used by | Key | Password | Key and password |
|---|---|---|---|---|
| OpenSSH `sftp` | Linux and macOS scripts | Yes | Yes | Yes |
| PuTTY `psftp` 0.78 | The engine inside WinSCP and FileZilla | Yes: ed25519, RSA | Yes | Yes |
| Paramiko 3.5 | Python scripts, Airflow | Yes: ed25519, RSA | Yes | No: use a key |
| Go `x/crypto/ssh` and `pkg/sftp` | Go services | Yes | Yes | Yes |
| SHA-1-only software, such as JSch 0.1.55 | Very old Java systems | No | No | No |

SHA-1 is refused on purpose. A partner on such software must upgrade.

Paramiko can't do the two-step login: it repeats a protocol message that Go's SSH library rejects. Give Paramiko partners `mode: any` with a key.

For what each client shows when an upload is refused, see [check files](../../guides/check-files/#what-a-partner-sees).
