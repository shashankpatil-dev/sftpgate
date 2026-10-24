# SFTPgate — Product Requirements Document

Status: Draft for review · Owner: @shashankpatil-dev · Last updated: 2026-10-24

## 1. Problem

Organizations exchange confidential files over SFTP every day: GST filing data, payment-gateway reconciliation files, bank statements, payroll exports, partner reports. Typical setups look like this:

- A hand-built OpenSSH VM with `chroot` jails, Unix users created by hand, and authorized_keys files nobody reviews.
- Cron scripts that copy uploaded files into S3/GCS, fail silently, and leave partial files for downstream jobs.
- IP allowlists split across iptables, cloud firewalls and tribal knowledge.
- No answer to the auditor's questions: "who downloaded this file, from which IP, and when?"
- Onboarding a new vendor takes days of tickets. Offboarding often never happens.

The SFTP *protocol* is not the problem. The problem is the **operations around it**: access control, storage routing, auditability and change management.

## 2. Product statement

**SFTPgate is a single container that turns one YAML file in git into a secure, audited SFTP endpoint backed by object storage.**

Every account, key, IP rule, folder and permission is declared in git and reviewed in a pull request. Onboarding a vendor means merging a PR, and offboarding means deleting lines.

## 3. Principles (apply to every decision)

1. **Strong, narrow purpose.** Secure file exchange with partners and internal services. Anything else is out.
2. **Git is the only source of truth.** No runtime edits through the UI or API. The UI is read-only.
3. **The best code is code never written.** Use proven libraries for SSH/SFTP/storage. Build only the glue that is our value.
4. **Secure by default.** No shell, no forwarding, no plaintext secrets in config, deny unless allowed.
5. **Boring to operate.** One binary, one config file, no database, stdout logs, Prometheus metrics.

## 4. Users

| Persona | Needs |
|---|---|
| **Platform / DevOps engineer** (operator) | Deploys the container, writes the YAML, wires storage, monitors it. |
| **Security / compliance** | Reviews access in PRs, gets audit trails, verifies IP and auth policies. |
| **External vendor / partner** (end user) | Connects with any standard SFTP client, uploads and downloads in their folders, nothing else. |
| **Internal service** (end user) | Automated jobs that push or pull files on schedule. |
| **Data / finance teams** (downstream) | Files land reliably in their bucket, complete and never partial. |

## 5. Core use cases

1. **Inbound from vendor:** the payment gateway uploads daily reconciliation CSVs to `/inbound`, and they land in `gs://recon-raw/razorpay/`.
2. **Outbound to vendor:** an internal job writes GST reports to `s3://reports/gst-partner/`, and the partner downloads them from `/outbound`.
3. **Internal exchange:** service A drops files that service B consumes from a bucket, with SFTP as the compatibility layer for legacy systems.
4. **Post-upload processing (v0.2):** decrypt a PGP file, copy it to a second bucket under a dated prefix, and notify a webhook.
5. **Scheduled pull (v0.3):** every day at 06:00 IST, fetch statements from the bank's own SFTP server into our bucket.
6. **Audit:** answer "show every access to `recon-raw/razorpay/2026-10-01.csv`" from logs.
7. **Test before prod:** run the same image and config locally against MinIO/fake-GCS with `docker compose up`.

## 6. Functional requirements

### 6.1 Configuration (v0.1)
- FR-1: The whole system is configured by one YAML file (`sftpgate.yaml`) with a versioned schema.
- FR-2: Secrets are never written inline. Values support `${env:NAME}` and `${file:/path}` references. This works with GitHub Actions secrets, k8s Secrets, Docker secrets and External Secrets Operator, because each of them delivers secrets as env vars or files.
- FR-35: `validate` **rejects** inline values in secret fields (password hashes, private keys, PGP keys and passphrases, webhook secrets, UI tokens). Public keys may be inline because they are not secret.
- FR-36 (v0.2): `${secret:<url>}` reads a value directly from GCP Secret Manager or AWS Secrets Manager / Parameter Store at load time, with credentials from the runtime environment.
- FR-3: Passwords are stored only as bcrypt hashes. The CLI provides `sftpgate hash-password`.
- FR-4: `sftpgate validate <file>` checks the schema, references, CIDRs, keys and patterns, and exits non-zero on error (for CI on PRs).
- FR-5: Hot reload on file change or SIGHUP. An invalid new config is rejected and the last good config keeps serving.
- FR-6: The running config version (content hash, and git SHA if provided) is exposed in metrics and the UI.

### 6.2 Authentication (v0.1)
- FR-7: Per-account public-key auth (multiple keys per account).
- FR-8: Per-account password auth (bcrypt).
- FR-9: Combination policy per account: `any` (key OR password) or `all` (key AND password, true two-factor).
- FR-10: Brute-force protection: temporarily block an IP after N failed attempts.
- FR-11: Persistent host keys loaded from a mounted secret (fingerprint stays stable across restarts).

### 6.3 Network access (v0.1)
- FR-12: Global IP/CIDR allowlist, checked at TCP accept before the SSH handshake.
- FR-13: Per-account IP/CIDR allowlist, checked at authentication.
- FR-14: Optional per-account limit on concurrent sessions.

### 6.4 Virtual folders and permissions (v0.1)
- FR-15: Each account sees a virtual tree of folders. Each folder maps to `storage + prefix`. Nothing outside is reachable.
- FR-16: Per-folder permissions: `list`, `download`, `upload`, `overwrite`, `delete`, `rename`, `mkdir`.
- FR-17: Per-folder filename allow patterns (e.g. `*.csv`, `*.pgp`) and max file size.
- FR-18: Only the SFTP subsystem is allowed. Shell, exec, SCP, port/agent/X11 forwarding are rejected.
- FR-19: Uploads become visible in storage **only when complete** (no partial files downstream).

### 6.5 Storage (v0.1)
- FR-20: Backends: GCS, S3 and S3-compatible (MinIO, R2, Ceph…), Azure Blob, and local disk, all through one URL format.
- FR-21: Cloud credentials come from the standard environment (workload identity, IAM role, env vars), never from the YAML.
- FR-22: Uploaded objects carry metadata: account, source IP, session ID, SHA-256.

### 6.6 Audit and observability (v0.1)
- FR-23: Every security-relevant event (connect, auth success/failure, every file operation, config reload) emits one structured JSON audit record to stdout.
- FR-24: Prometheus `/metrics` endpoint. `/healthz` and `/readyz` endpoints.
- FR-25: The docker-compose bundle ships Prometheus and Grafana with a pre-built dashboard (v0.2).
- FR-34: Optional OpenTelemetry traces (v0.2): one trace per session, with spans for each file operation, storage call and post-upload action, exported over OTLP to the org's tracing backend. Off unless configured.

### 6.7 Post-upload actions (v0.2)
- FR-26: Per-folder ordered `on_upload` actions: `pgp_decrypt`, `pgp_encrypt`, `copy` (to another storage/prefix with templates like `{date}`), `webhook`.
- FR-27: Actions run asynchronously with retries. Pending work survives a restart. After the final failure the job is recorded as failed and visible in metrics and logs.
- FR-28: The original upload is never modified by actions (the landing zone is immutable from SFTPgate's side).

### 6.8 Read-only UI and API (v0.3)
- FR-29: Read-only web UI: live sessions, recent transfers, recent auth failures, action status, effective config (secrets redacted), config version.
- FR-30: Read-only JSON API with the same data. Protected by a bearer token.
- FR-31: No endpoint can change state.

### 6.9 Scheduled transfers (v0.3)
- FR-32: Cron-scheduled `pull` (remote SFTP to storage) and `push` (storage to remote SFTP) jobs with host-key pinning.

### 6.10 GitOps delivery (v0.3)
- FR-33: A documented pattern for auto-syncing config from a git repo using the standard `git-sync` sidecar (no git client inside SFTPgate).

## 7. Non-functional requirements

| ID | Requirement |
|---|---|
| NFR-1 | Memory use is independent of file size (streaming, no full-file buffering in RAM). |
| NFR-2 | Supports files up to at least 10 GiB (bounded by the spool volume). |
| NFR-3 | 200 concurrent sessions on 1 vCPU / 512 MiB without errors (target, measured in v0.1 load test). |
| NFR-4 | Startup under 2 s. Config reload under 1 s with no dropped unaffected sessions. |
| NFR-5 | Container image: distroless, non-root, read-only root filesystem, under 40 MB. |
| NFR-6 | Works with common clients: OpenSSH `sftp`, WinSCP, FileZilla, Paramiko, JSch, Cyberduck. |
| NFR-7 | No database. Durable state is limited to host keys (secret), the spool and action queue (volume), and objects (storage). |
| NFR-8 | License: Apache-2.0. |

## 8. Non-goals

Stated explicitly so they don't creep in:

- FTP, FTPS, WebDAV, HTTP upload portals.
- Editing anything through the UI or API, and user self-service (password resets, key uploads).
- LDAP/AD/OIDC for SFTP logins (accounts are declared in git).
- A database, multi-tenant SaaS mode, or a Kubernetes operator.
- Resumable or append uploads, symlinks, hard links, chmod/chown semantics.
- A built-in git client (use `git-sync`).
- Virus scanning, DLP or content inspection (can be done downstream from the bucket).
- Cross-folder atomic rename.

## 9. Success metrics

- Onboarding a new vendor takes **1 PR, under 15 minutes**, end to end.
- **Zero partial files** observed downstream.
- Every file access is answerable from the audit log in **under 5 minutes**.
- `docker compose up` to a first successful upload in **under 5 minutes** for a new user.
- Core codebase (excluding tests) stays **under ~3,000 lines of Go** at v0.1.

## 10. Releases

| Version | Scope |
|---|---|
| **v0.1 (MVP)** | FR-1 to FR-24, FR-35: config, validate, reload, auth (key/password/both), IP allowlists, throttling, virtual folders, permissions, patterns, size limits, object storage, atomic uploads, JSON audit, metrics, health, Dockerfile, compose with MinIO and fake-GCS. |
| **v0.2** | FR-25 to FR-28, FR-34, FR-36: cloud secret manager references, on_upload actions (PGP, copy, webhook), durable queue, Grafana dashboard, OpenTelemetry traces. |
| **v0.3** | FR-29 to FR-33: read-only UI and API, scheduled pull/push, git-sync pattern. |

## 11. Open questions for review

1. Account naming: is one account = one SFTP login enough, or do partners need several logins sharing folders? (Proposed: one login per account. Duplicate YAML via anchors if needed.)
2. Should the `all` auth mode allow ordering (password first, then key)? (Proposed: key first, then password, which is the common client behaviour.)
3. Do we need SSH certificate auth (CA-signed user keys) for internal services? (Proposed: later, only if asked.)
4. Do legacy vendor clients (old JSch, `ssh-rsa` SHA-1) need to be supported via an explicit opt-in flag? (Proposed: opt-in `server.legacy_algorithms`, default off.)
