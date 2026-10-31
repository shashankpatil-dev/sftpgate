---
title: Config overview
description: The layout of sftpgate.yaml, and how values are written.
---

Everything SFTPgate does comes from one file, `sftpgate.yaml`. Settings you leave out take their default. To see every setting in one commented file, go to the [complete example](../complete-example/).

```yaml
version: 1              # required, always 1
server: {...}           # how the SFTP server behaves
http: {...}             # metrics, health, API and admin calls
storages: {...}         # your buckets
actions: {...}          # named actions (check, PGP, copy, webhook)
accounts: [...]         # partner and service logins
remotes: {...}          # other SFTP servers, for scheduled transfers
transfers: [...]        # scheduled pulls and pushes
```

| Section | Page |
|---|---|
| `server` | [Server](../server/) |
| `http` | [HTTP](../http/) |
| `storages` | [Storages](../storages/) |
| `accounts` | [Accounts](../accounts/) and [folders](../folders/) |
| `actions` | [Actions](../actions/) and [hooks](../hooks/) |
| `remotes` | [Remotes](../remotes/) |
| `transfers` | [Transfers](../transfers/) |

## How values are written

| Kind | Written as | Example |
|---|---|---|
| Duration | A number and a unit | `30s`, `15m`, `2h` |
| Size | A number and a binary unit | `512KiB`, `500MiB`, `2GiB` |
| IP range | CIDR, or a single address | `10.0.0.0/8`, `13.232.10.4` |
| Secret | A reference | `${env:NAME}`, `${file:/path}`, `${secret:URL}` |
| Schedule | Cron, in `server.timezone` | `0 6 * * *` |

See [secrets](../../deploy/secrets/) for how references work.

## Check a config

```sh
sftpgate validate -offline sftpgate.yaml
```

Without `-offline`, it also checks that secrets, files and storages can be reached. See the [CLI](../cli/).

## Reloading

SFTPgate checks the file every `reload_interval` and on `SIGHUP`. A valid new config goes live right away, and only sessions of accounts that changed are disconnected. An invalid file is rejected, and the old config keeps running.
