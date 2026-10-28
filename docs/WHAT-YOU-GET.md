# What you get with SFTPgate

What your organization has once SFTPgate is running, for partners, for your own
teams, and for security and compliance.

How to set it up: [ONBOARDING.md](ONBOARDING.md). Terms: [GLOSSARY.md](GLOSSARY.md).

---

## 1. In one picture

```
 Partners and services                 SFTPgate                       Your cloud
 ─────────────────────        ──────────────────────────────        ─────────────────
 Bank, payment gateway,  SFTP   checks who, from where, what   saves  GCS / S3 / Azure
 GST provider, your  ─────────▶ only their folders          ─────────▶ buckets
 own older systems              complete files only               (all the data)
                                     │
                                     ├─▶ optional: decrypt, copy, notify
                                     ├─▶ optional: fetch from or send to partners on a schedule
                                     └─▶ audit log, metrics, alerts, traces, read-only UI
```

SFTPgate is a **bridge with a guard**. The data always lives in your buckets.
SFTPgate only holds a file for the few seconds it takes to arrive completely.

---

## 2. What partners get

- **A normal SFTP server.** Any standard tool works: WinSCP, FileZilla, the
  `sftp` command, Python (Paramiko), Java (JSch). Nothing special to install.
- **Their own folders only,** like `/inbound` and `/outbound`. They can't see
  other partners or anything else.
- **Clear answers when something is refused:** "Permission denied", "file
  already exists", "out of upload space, try again later". No silent failures.
- **A stable server identity.** The host key fingerprint you give them stays
  the same, so they know they're connected to the real you.

## 3. What your organization gets

### Onboarding by pull request
- Adding a partner is a **few lines of YAML** in a pull request: their key,
  their IPs, their folders. Review, merge, and it's live in seconds.
- Removing a partner is **deleting those lines**. Their open sessions are closed.
- Every change has an author, a reviewer, a date and a reason, in git history.
- CI catches mistakes before merge (`sftpgate validate`), and a broken config
  is never applied: the old one keeps running.

### Files land complete, in your buckets
- **No half files, ever.** A file reaches the bucket only after the partner
  finishes it. A dropped connection leaves nothing behind.
- **Proof of integrity.** Each file's SHA-256 is stored with it. After every
  save, SFTPgate reads the object's size and checksum back from the bucket, and
  only then tells the partner the upload worked. GCS and Azure also check an
  MD5 while saving; S3 checks each request with its own checksums. A file
  damaged or lost on the way is never reported as saved.
- **Who sent it, from where:** the account, IP and session are stored with
  every file.
- **No accidental overwrites.** Without the `overwrite` permission, a file can
  never be replaced, even if two partners upload the same name at once.
- **Your choice of storage:** GCS, S3, S3-compatible (MinIO, R2, Ceph), Azure
  Blob, or local disk. Several at once, chosen per folder.

### Optional automation (off until you add it)

| Feature | What it saves you |
|---|---|
| **Checks** on every file (size, CSV header, rows, columns), before it's stored or sent | A broken report is refused at the door instead of breaking reconciliation later |
| **PGP decrypt**, with optional signature check | No more scripts decrypting bank files; unsigned or forged files are rejected |
| **PGP encrypt** | Files encrypted for a recipient before they're stored or sent |
| **Copy** to another bucket, with names like `razorpay/2026-10-24/file.csv` | Files sorted into your data lake or warehouse automatically |
| **Webhook** to your systems, signed | Your systems know the moment a file is uploaded, downloaded, pushed or pulled |
| **Scheduled pull** from a partner's SFTP server | "Fetch the bank statements every morning at 6" with no cron scripts |
| **Scheduled push** to a partner's SFTP server | "Send prepared payout files every 30 minutes, then archive them" |

Each is a named **action** defined once and run from the **hooks** of any folder
or transfer (before or after an upload, download, push or pull). Actions of
"after" hooks are **retried automatically** with growing waits, survive
restarts, and are visible in the UI and alerts if they give up.

### Seeing what's happening
- **Read-only web UI** (`/ui/`): open sessions, recent events with filters,
  action jobs, and the running config and its git commit. It has no edit
  buttons, by design.
- **JSON API** (`/api/v1/`): the same data for scripts and other tools.
- **Grafana dashboard** (19 panels): logins, connections, uploads and downloads
  per partner, data moved, transfer times, storage errors, denied requests,
  action jobs, scheduled transfers, and the config version.
- **Tracing (optional):** each session and upload as a timeline in Jaeger,
  Tempo or Datadog, continued into your webhook receivers.

### Being told when something is wrong

11 ready-made alert rules. Prometheus shows them as firing; to send Slack,
email or PagerDuty messages, connect it to Alertmanager (on Kubernetes your
Prometheus Operator usually has one already).

| Alert | Tells you |
|---|---|
| `SftpgateDown` | SFTPgate isn't answering |
| `SftpgateNotReady` | It's running but has no config |
| `SftpgateLoginFailureSpike` | More than 20 failed logins in 5 minutes: someone may be guessing |
| `SftpgateIPsBlocked` | IPs are being blocked after failed logins |
| `SftpgateStorageErrors` | A bucket is failing |
| `SftpgateConfigReloadFailed` | A new config was rejected (the old one keeps running) |
| `SftpgateSpoolDiskLow` | The upload disk is getting full |
| `SftpgateActionsRetrying` | Actions are failing and being retried |
| `SftpgateActionsGaveUp` | An action job gave up |
| `SftpgateActionsNotQueued` | A file was saved but its steps couldn't be queued |
| `SftpgateTransferFailing` | A scheduled transfer failed in the last hour |

## 4. What security and compliance get

- **An audit trail.** One JSON line for every connection, login (and failed
  login), upload, download, delete, rename, action job, transfer and config
  change: who, what, when, from which IP, and the result. Send it to a
  retention-locked bucket and it can't be altered.
- **Layered access checks,** in this order:
  1. a server-wide IP allowlist and connection limit;
  2. blocking of IPs that keep failing to log in;
  3. modern encryption only (no SHA-1, for anyone);
  4. an account-level IP allowlist;
  5. key, password, or both (two-factor);
  6. SFTP only: no shell, no commands, no tunnels;
  7. the folder jail, permissions, filename rules and size limits.
- **No secrets in git.** The config only holds references; `validate` refuses
  plain secrets. Passwords are stored as bcrypt hashes only.
- **No cloud keys needed** on Kubernetes (workload identity), and least-privilege
  bucket access.
- **A hardened container:** minimal image, non-root user, read-only filesystem,
  no Kubernetes API access.
- **Reviewed change control:** all access changes go through git pull requests.

## 5. What operations get

| You get | Meaning |
|---|---|
| **One container** | Docker on one server, or Helm on any Kubernetes cluster |
| **No database** | Nothing to back up, migrate or patch besides the container and the config |
| **Hot reload** | Config and secret changes without restarts or dropped partners |
| **Health checks** | `/healthz` and `/readyz` for Docker and Kubernetes; unhealthy copies are restarted, unready ones get no partners |
| **Graceful shutdown** | Upgrades wait for uploads in progress; nothing is saved half-way |
| **Scale out** | Several copies behind one address on Kubernetes; scheduled transfers still run once |
| **Built-in limits** | Connection, session, file-size and disk-space limits protect the server |
| **Local test setup** | `make up` runs the whole thing with test storage and monitoring on a laptop |

## 6. Where to find everything

| What | Where |
|---|---|
| SFTP, for partners | port 22 (Kubernetes) or the port you map (Docker) |
| Web UI | `http://<host>:8080/ui/` (needs `http.tokens`) |
| API | `http://<host>:8080/api/v1/status`, `/sessions`, `/events`, `/actions`, `/config` |
| Metrics | `http://<host>:8080/metrics` |
| Health | `http://<host>:8080/healthz`, `/readyz` |
| Audit log | the container's output (`docker logs`, `kubectl logs`, or your log system) |
| Dashboard and alerts | `deploy/helm/sftpgate/files/` (installed by Helm with `metrics.*` values) |
| Failed action jobs | `<spool_dir>/queue/failed/` on the SFTPgate disk, and the UI's Actions page |

## 7. What SFTPgate deliberately does not do

These keep it small, safe and focused:

- **No FTP, FTPS or web upload page.** SFTP only.
- **No editing in the UI.** All changes go through git.
- **No partner self-service** (password resets, key uploads). You control access.
- **No file storage of its own.** Your buckets keep the data.
- **No old, weak encryption** (SHA-1). Partners on very old software must upgrade.
- **No virus scanning or content inspection.** Do that downstream, from the
  bucket or from the webhook.
- **One view for all accounts in the UI.** It's built for the operations team,
  not for each partner.
