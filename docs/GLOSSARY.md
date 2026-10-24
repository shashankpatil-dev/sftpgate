# SFTPgate Glossary

Every term used in SFTPgate, its config, and its docs, in plain words.
Terms are grouped by topic. Each group is in alphabetical order.

Related docs: [ONBOARDING.md](ONBOARDING.md) (how to set it up) and
[WHAT-YOU-GET.md](WHAT-YOU-GET.md) (what it does for you).

---

## 1. The big picture

**SFTPgate**
: The product. One small program that partners connect to with SFTP. It checks
  who they are, lets them reach only their own folders, and puts their files into
  your cloud storage (or hands files from storage to them). Think of it as a
  security guard and a delivery desk standing between outside companies and
  your storage.

**SFTP**
: "SSH File Transfer Protocol". A standard, encrypted way to send and receive
  files. Banks, payment gateways and tax portals use it everywhere. Tools like
  WinSCP, FileZilla and the `sftp` command speak it.

**SSH**
: "Secure Shell". The encrypted connection that SFTP runs inside. SSH can also
  give someone a command line on a server. SFTPgate never allows that: only
  file transfer.

**Gateway**
: Something that stands at the edge and controls what passes through. SFTPgate
  is a gateway between partners and your storage. It keeps no files of its own
  for long.

**Partner / vendor**
: An outside company that exchanges files with you, for example a payment
  gateway sending settlement files or a GST provider collecting returns.

**Internal service**
: One of your own systems that sends or picks up files over SFTP, often an
  older system that can't write to cloud storage directly.

**Account**
: One SFTP login. Usually one per partner or service. The account decides how
  the partner logs in, from which IPs, and which folders it sees.

**Bridge**
: SFTPgate's role: it connects the SFTP world (partners) to the bucket world
  (your storage) without storing the data itself.

---

## 2. Config and GitOps

**Config / config file / `sftpgate.yaml`**
: The single file that describes everything: accounts, folders, storage,
  rules. SFTPgate does exactly what this file says, nothing else.

**YAML**
: The text format of the config file. Indentation (spaces) shows what belongs
  to what. Tabs are not allowed.

**Git / repository / repo**
: Version control. Your config lives in a git repository, so every change has
  an author, a date, a review and a history.

**Pull request (PR)**
: A proposed change in git that others review before it's accepted. Adding a
  partner = a pull request that adds a few lines to `sftpgate.yaml`.

**Merge**
: Accepting a pull request, so the change becomes part of the main version.

**GitOps**
: Running a system from git: the repository is the truth, and whatever is
  merged is what runs. Nobody changes the live system by hand.

**Source of truth**
: The one place that decides how things should be. For SFTPgate that's the
  config in git, never the UI.

**Reload / hot reload**
: Picking up a changed config **without restarting**. SFTPgate checks the file
  every few seconds (`reload_interval`). When it changes, the new config goes
  live, and only the partners whose account changed are disconnected.

**Rejected reload**
: If the new config has a mistake, SFTPgate refuses it, logs why, and **keeps
  running the old config**. A typo never takes the service down.

**Validate / `sftpgate validate`**
: Checking a config file for mistakes without running it. It lists every
  problem with the exact place, like `accounts[0].auth.mode: must be any or all`.

**Offline validation (`-offline`)**
: Validation that skips anything needing real secrets, files or the network.
  Used in CI on pull requests, where production secrets aren't available.

**CI (continuous integration)**
: Automatic checks that run on every pull request, like tests and
  `sftpgate validate`. A broken config is caught before it's merged.

**Config hash**
: A short fingerprint of the config file's exact contents. It changes when any
  character changes, so you can tell which version is running. Shown in the UI
  and metrics.

**Revision**
: The git commit (a 40-character id) the running config came from. Shown in the
  UI and metrics when known.

**git-sync**
: A small standard helper program that copies a git repository onto disk and
  keeps it up to date. SFTPgate reads its config from there. This is how
  "merge = live" works.

**Fingerprint (of an account)**
: An internal summary of one account's settings. On reload, SFTPgate compares
  fingerprints to know which partners' settings changed, and only those
  partners are disconnected.

**Default**
: The value used when the config doesn't set something. For example
  `idle_timeout` defaults to 15 minutes.

**Off by default / opt-in**
: Extra features (PGP, copies, webhooks, transfers, UI, tracing) do nothing
  until you add their settings. A minimal config gives a plain, locked-down
  SFTP gateway.

---

## 3. Logging in (authentication)

**Authentication / auth**
: Proving who you are when connecting.

**Public key / private key (key pair)**
: Two matching files made together. The **private key** stays with the
  partner and is never shared. The **public key** is given to you and goes in
  the config. A login works only with the matching private key. Safer than
  passwords.

**`ssh-ed25519`, `ssh-rsa`, ECDSA**
: Types of keys. `ed25519` is the modern, recommended type. RSA keys still work,
  but only with modern (SHA-2) signatures.

**authorized_keys format**
: The usual one-line way of writing a public key:
  `ssh-ed25519 AAAAC3Nza... name`. That's what goes into `public_keys`.

**Password**
: The classic login secret. SFTPgate never stores passwords themselves, only
  their **hash**.

**Hash / bcrypt hash / `password_hash`**
: A one-way scramble of a password. You can check a password against it, but
  you can't get the password back from it. bcrypt is a slow-on-purpose hash, so
  guessing is very slow. Make one with `sftpgate hash-password`. It looks like
  `$2a$12$...`.

**Cost (bcrypt cost)**
: How slow the bcrypt hash is. Higher is safer but slower. SFTPgate requires
  at least 10 and uses 12 by default.

**Auth mode: `any`**
: The partner can log in with **either** its key **or** its password.

**Auth mode: `all`**
: The partner needs its key **and** its password: two-factor login. The key is
  checked first, then the password.

**Two-factor / 2FA**
: Needing two different proofs to log in, here something you have (the key)
  and something you know (the password).

**Host key**
: The **server's own** key pair. It proves to partners that they're talking to
  the real SFTPgate and not an impostor. Partners save its fingerprint the
  first time. **Never change it casually:** partners will see a warning that
  looks like an attack.

**Host key fingerprint**
: A short summary of the host key (like `SHA256:tqtLs8...`) that you send to
  partners so they can check it.

**Key fingerprint (`key_fp`)**
: The same idea for a partner's key. The audit log records which of a
  partner's keys was used.

**Pinned host key (`host_key` in `remotes`)**
: When SFTPgate connects **out** to a partner's server (scheduled transfers),
  it accepts only the exact host key you wrote down. Any other key is refused,
  which stops man-in-the-middle attacks.

**Man-in-the-middle**
: An attacker secretly sitting between two parties, pretending to be each to
  the other. Host keys and pinning prevent it.

**SHA-1 / legacy algorithms**
: Old, weak cryptography. SFTPgate refuses it completely, both for partners
  connecting in and for its own outgoing connections. Partners on very old
  software must upgrade.

**Username enumeration**
: An attacker learning which usernames exist from how quickly or differently
  the server answers. SFTPgate answers unknown users the same way, with the
  same timing.

---

## 4. Network protection

**IP address / IP**
: The number that identifies a computer on the network, like `52.66.1.9`.

**CIDR / range**
: A way to write a group of IPs: `52.66.0.0/16` means every IP starting with
  `52.66.`. A single IP like `13.232.10.4` is also allowed.

**Allowlist / `allow_ips`**
: The list of IPs that may connect. Everything else is refused.
  - `server.allow_ips` is checked first, for every connection, before any
    login work.
  - `accounts[].allow_ips` is checked per account, during login.
  - An empty list means "no IP limit at this level".

**Throttle / blocking**
: After too many failed logins from one IP (`max_failures` within `window`),
  that IP is blocked for a while (`block`). This stops password guessing. A
  correct login doesn't reset the count.

**`max_auth_tries`**
: How many login attempts one connection may make before it's closed. Default 6,
  the same as OpenSSH, because clients often try several keys.

**`max_connections`**
: The most connections open at once, counting all accounts together. Protects
  the server from a flood. Default 1000.

**`max_sessions`**
: The most connections open at once for one account. 0 means no limit.

**Session**
: One logged-in connection, from login to disconnect.

**Idle timeout (`idle_timeout`)**
: A connection with no activity for this long is closed. Default 15 minutes.

**Banner**
: An optional message shown to partners before they log in, like "Authorized
  use only".

**Port**
: A numbered "door" on a server. SFTPgate listens for SFTP on 2222 inside its
  container, which is usually shown to the world as port 22. Its web endpoints
  use 8080.

**Load balancer**
: In the cloud, the public address partners connect to. It forwards
  connections to SFTPgate. With several copies of SFTPgate it spreads partners
  across them.

**Real client IP / `externalTrafficPolicy: Local`**
: A Kubernetes setting that makes the load balancer pass on the partner's real
  IP. Without it, every connection would seem to come from the load balancer,
  and IP allowlists couldn't work.

---

## 5. Folders and permissions

**Virtual folder / folder**
: What a partner sees after logging in, like `/inbound` or `/outbound`. It
  isn't a real folder on a server: it's a window onto a part of a bucket.

**Root (`/`)**
: The top level a partner sees. It only lists the account's folders, and
  nothing can be uploaded there.

**Folder jail**
: A partner can never get outside its own folders. Tricks like `../../` are
  cleaned away, so they only ever reach their own area.

**`path`**
: The folder's name as the partner sees it, like `/inbound`. One level only.

**`storage` (in a folder)**
: Which storage (bucket) the folder's files live in, by name from `storages`.

**`prefix`**
: The "sub-folder" inside the bucket where this folder's files go, like
  `razorpay/`. Two partners can share one bucket with different prefixes.

**Permissions / `perms`**
: What a partner may do in a folder:

  | Permission | Meaning |
  |---|---|
  | `list` | see the folder's contents |
  | `download` | fetch files |
  | `upload` | send new files |
  | `overwrite` | replace a file that already exists |
  | `delete` | remove files (and empty sub-folders) |
  | `rename` | rename files inside the folder |
  | `mkdir` | create sub-folders |

**Overwrite protection**
: Without `overwrite`, an existing file can never be replaced, not even when
  two partners upload the same name at the same moment. The bucket itself
  refuses the second save, and that partner gets "file already exists".

**Patterns / `patterns`**
: Allowed file names, using wildcards: `*.csv` means "anything ending in
  .csv". Other names are refused. No patterns means any name is allowed.

**Wildcard**
: `*` matches any text and `?` any single character, so `report-?.csv`
  matches `report-1.csv`.

**`max_file_size`**
: The largest file allowed in a folder, like `500MiB`. Bigger uploads are
  refused, and the file never reaches the bucket.

**Size units (B, KiB, MiB, GiB)**
: Bytes, kibibytes (1024 bytes), mebibytes (1024 KiB) and gibibytes (1024 MiB).
  `500MiB` is about 524 million bytes.

**Setstat**
: An SFTP request to change a file's time or permissions. Buckets don't have
  those, so SFTPgate says "OK" and does nothing. That keeps tools like WinSCP
  happy.

**Directory marker**
: Buckets have no real folders. When a partner creates a sub-folder, SFTPgate
  stores an empty object whose name ends in `/`, the usual way to show a
  folder in a bucket.

---

## 6. Storage

**Bucket / object storage**
: Cloud storage for files, like Google Cloud Storage (GCS), Amazon S3 or Azure
  Blob Storage. Files are called **objects**, and each has a name called a
  **key**.

**Object / key**
: A stored file and its full name in the bucket, like
  `razorpay/2026-10-24.csv`.

**`storages`**
: The config's list of buckets, each with a short name you choose and a URL:

  | URL | Storage |
  |---|---|
  | `gs://my-bucket` | Google Cloud Storage |
  | `s3://my-bucket?region=ap-south-1` | Amazon S3 |
  | `s3://my-bucket?endpoint=https://minio.internal&use_path_style=true` | S3-compatible (MinIO, Ceph, Cloudflare R2) |
  | `azblob://my-container` | Azure Blob Storage |
  | `file:///var/lib/sftpgate/data` | a local folder (testing, small setups) |
  | `mem://` | memory only, lost on restart (tests) |

**Credentials (cloud)**
: The permission for SFTPgate to use a bucket. These never go in the config.
  They come from the environment: **workload identity** on Kubernetes, an IAM
  role on a VM, or standard variables like `AWS_ACCESS_KEY_ID` or
  `GOOGLE_APPLICATION_CREDENTIALS`.

**Workload identity / IRSA**
: A cloud feature that gives a Kubernetes pod its own identity, so the bucket
  trusts the pod directly with no keys stored anywhere. GKE calls it Workload
  Identity, AWS calls it IRSA, and Azure calls it Workload Identity too.

**IAM / least privilege**
: The cloud's permission system. "Least privilege" means giving SFTPgate access
  only to the buckets it needs, and nothing else.

**Metadata**
: Extra labels stored with each object. SFTPgate adds `sftpgate-account`,
  `sftpgate-ip`, `sftpgate-session` and `sftpgate-sha256`, so you can always
  see who sent a file and check it wasn't changed.

**Checksum / SHA-256 / MD5**
: A fingerprint of a file's contents. If even one byte changes, the
  fingerprint changes. SFTPgate stores the SHA-256 with every upload, and the
  bucket checks the MD5 when saving, so a file damaged on the way is never
  stored.

**Integrity**
: Being sure a file is exactly what was sent, with no missing or changed bytes.

**Emulator / mock**
: A fake storage service for local testing (`fake-gcs-server`, `s3mock`). It
  behaves like the real thing, so no cloud account is needed.

---

## 7. How uploads and downloads work

**Upload**
: A partner sending a file to you.

**Download**
: A partner fetching a file from you.

**Open / write / close**
: The three steps every SFTP upload has, all done by the partner's software
  on its own:
  1. **Open:** "I'm starting `settlement.csv`".
  2. **Write:** the content, in pieces.
  3. **Close:** "that was the last piece".

  The file is complete only at **close**.

**Chunk**
: One piece of a file. Large files travel in many chunks, and they can arrive
  out of order.

**Spool**
: The **waiting room** for uploads. While a partner is still sending, the
  pieces collect in a temporary file on SFTPgate's own disk. At **close**, the
  finished file goes to the bucket in one step and the temp file is deleted.
  Files never stay in the spool.

**`spool_dir`**
: The folder used as the spool. Default `/var/lib/sftpgate/spool`.

**Spool disk**
: The disk the spool lives on. Size it as roughly **largest `max_file_size` ×
  uploads at the same time**, plus some room. Small CSV files need almost
  nothing.

**`min_spool_free`**
: How much free space must always be left on the spool disk. Default 1 GiB.

**Space reservation**
: When an upload opens, SFTPgate sets aside that folder's `max_file_size` on
  the spool disk. If it doesn't fit next to the uploads already running, the
  upload is refused at the start ("out of upload space, try again later")
  instead of breaking halfway.

**Commit / commit on close**
: The moment the finished file is saved into the bucket. Before that, nothing
  is visible in the bucket.

**Partial file / half file**
: A file that's only partly there because the transfer broke. **SFTPgate never
  lets one reach the bucket**: if the partner disconnects before close, the
  upload is thrown away.

**Atomic**
: Happens completely or not at all, never halfway. Saving to the bucket is
  atomic.

**Streaming**
: Passing data along while it's still arriving, without storing it first.
  Downloads stream straight from the bucket. Uploads go through the spool
  first, for the reasons above.

**Range read**
: Reading part of a file, from a given position. That's how downloads stream
  from the bucket chunk by chunk.

**Listing**
: Showing a folder's contents (`ls`). Big folders are read from the bucket
  page by page, so any size works.

**Race / race condition**
: Two things happening at almost the same time, where the order matters, like
  two partners uploading the same file name together. SFTPgate handles this
  safely (see overwrite protection).

**Idempotent**
: Safe to do twice. Running a `copy` step twice just writes the same file
  again.

---

## 8. Post-upload actions

**Post-upload actions / `on_upload`**
: Steps that run automatically after a file is saved in a folder, in order,
  like "decrypt it, copy it to the warehouse bucket, then tell the recon
  system". The uploaded original is never changed.

**Step**
: One action in `on_upload`. Each step is exactly one of `pgp_decrypt`,
  `pgp_encrypt`, `copy` or `webhook`.

**Landing file / landing object**
: The file exactly as the partner uploaded it. Steps read it but never
  change it.

**Job**
: One saved file waiting for (or going through) its post-upload steps.

**Queue / action queue**
: The list of jobs waiting to run, kept as small files on disk
  (`<spool_dir>/queue`). Because they're on disk, a restart doesn't lose them.

**Worker / `workers`**
: A background helper that takes jobs from the queue and runs them. Several
  run at once. Default 4.

**Retry**
: Trying a failed job again later.

**Backoff / `backoff`**
: The wait before a retry, which doubles each time: 30s, 1m, 2m, 4m… up to
  1 hour. This gives a broken system time to recover.

**`max_attempts`**
: How many times a job is tried before SFTPgate gives up. Default 8.

**Failed job / `queue/failed`**
: A job that gave up. Its file is safe in the bucket but wasn't processed.
  Move its job file back into `queue` to run it again.

**Permanent failure**
: A failure that retrying can't fix, like a folder removed from the config, a
  file changed in the bucket, or a file that can't be decrypted. It goes to
  `failed` at once.

**At-least-once**
: A job always runs, but in rare cases (after a crash or retry) it can run
  twice. Webhook receivers should ignore a `job_id` they've already seen.

**`job_id`**
: The unique id of a job, like `20261024T075812Z-1a2b3c4d`. It starts with
  the date and time.

**Template / placeholder (`{date}` and others)**
: Words in curly braces inside a key, filled in for each file. In `copy` keys:
  `{account}`, `{folder}`, `{date}` (2026-10-24), `{time}` (153012),
  `{filename}`, `{job_id}`. In pull transfer keys: `{name}`, `{date}`,
  `{time}`, `{filename}`.

**`timezone`**
: Which time zone `{date}`, `{time}` and transfer schedules use, like
  `Asia/Kolkata`. Default UTC.

### PGP

**PGP / OpenPGP / GPG**
: A standard way to encrypt and sign files. Many banks and tax systems send
  and expect PGP files (`.pgp`, `.gpg`, `.asc`).

**Encrypt / decrypt**
: Scrambling a file so only the holder of the right private key can read it
  (encrypt), and turning it back into readable form with that key (decrypt).

**`pgp_decrypt`**
: A step that decrypts uploaded PGP files with **your** private key.
  `.pgp`, `.gpg` and `.asc` are removed from the name.

**`pgp_encrypt`**
: A step that encrypts a file for one or more **recipients'** public keys and
  adds `.pgp` to the name.

**Recipients**
: The people or systems a file is encrypted for. Only they can open it.

**Passphrase**
: The password protecting a PGP private key.

**Armored**
: A PGP key or file written as text (starting with `-----BEGIN PGP ...`)
  instead of binary. Both kinds are accepted.

**Signature / signing**
: A seal the sender adds with their private key, proving the file came from
  them and wasn't changed.

**`signer_keys`**
: The partner's public keys. When set, a file must carry a valid signature
  from one of them. Unsigned files, unknown signers and bad signatures are
  rejected.

### Copy and webhook

**`copy`**
: A step that saves the current file to another storage under a templated
  key, like `warehouse/{account}/{date}/{filename}`.

**Webhook**
: A web call SFTPgate makes to another system to say "a file arrived". It's a
  POST with JSON describing the file and the copies made.

**HMAC signature / `X-Sftpgate-Signature`**
: With a webhook `secret`, SFTPgate signs the message as
  `sha256=<code>`. The receiver recomputes it with the same secret to prove the
  call really came from SFTPgate and wasn't changed.

---

## 9. Scheduled transfers

**Scheduled transfer / `transfers`**
: SFTPgate acting as the **client**: on a schedule, it connects to a
  partner's SFTP server and either fetches files (pull) or sends files (push).

**Remote / `remotes`**
: A partner's SFTP server that SFTPgate connects to: its address, username,
  pinned host key, and SFTPgate's key or password for it.

**Pull**
: Fetch new matching files from the remote folder into a bucket. Files already
  in the bucket are skipped. With `delete_after`, the partner's copy is removed
  once it's stored.

**Push**
: Send files from a bucket prefix to the remote folder. Afterwards (`after`):
  - `keep`: leave the file, and skip files the remote already has.
  - `delete`: remove it from the bucket.
  - `move:<prefix>`: move it to another prefix, like `sent/`.

**Temporary name (dot file)**
: Pushes upload as `.name` first and rename to `name` when complete, so the
  partner never picks up half a file.

**Cron / `schedule`**
: The standard way to write "when": `0 6 * * *` means every day at 06:00, and
  `*/30 * * * *` every 30 minutes. `@every 5m` also works.

**Run**
: One execution of a transfer. A run is skipped if the previous one is still
  going.

**Replica / `SFTPGATE_TRANSFERS_ONLY_ON`**
: With several copies of SFTPgate running, transfers must run on only one of
  them, or files would be fetched twice. The Helm chart arranges this for you.

---

## 10. Secrets

**Secret**
: Anything that must stay private: passwords, private keys, PGP keys,
  webhook secrets, UI tokens.

**Inline secret**
: A secret written directly into the config file. **SFTPgate refuses this**,
  because the config lives in git, where many people can read it.

**Reference / `${...}`**
: A pointer to where the secret really is. SFTPgate looks it up when loading:

  | Reference | Reads from |
  |---|---|
  | `${env:NAME}` | an environment variable |
  | `${file:/path}` | a file, often a mounted Kubernetes or Docker secret |
  | `${secret:gcpsecretmanager://projects/P/secrets/S}` | Google Secret Manager |
  | `${secret:awssecretsmanager://NAME?region=R}` | AWS Secrets Manager |
  | `${secret:awsparamstore://NAME?region=R}` | AWS Parameter Store |

**Environment variable / env var**
: A named value given to a program when it starts, like `UI_TOKEN=...`.

**Kubernetes Secret / Docker secret**
: The platform's safe place for secrets. Its values reach SFTPgate as env vars
  or as files.

**Secret rotation**
: Replacing a secret with a new one. Secrets in files (`${file:}`) are picked up
  by themselves within seconds. Cloud secret stores (`${secret:}`) are re-read on
  `SIGHUP` or restart.

**SIGHUP**
: A signal that tells SFTPgate to reload right now and re-read every secret.
  `kill -HUP <pid>` or `docker kill -s HUP <container>`.

---

## 11. Web UI, API and tokens

**Web UI**
: Read-only web pages at `/ui/`. They show open sessions, recent events,
  post-upload jobs and the running config. It's for the operations and
  security team, not for partners.

**API**
: The same information as JSON at `/api/v1/...`, for scripts and tools.

**Read-only**
: Can look, can't change. There are no buttons to change anything: changes go
  through git.

**Token / `http.tokens`**
: The password for the UI and API. At least 16 characters, given as a
  reference. With no tokens, the UI and API are switched off.

**Basic auth / Bearer token**
: Two ways to send the token. Browsers show a login box (any username, token
  as password). Scripts send the header `Authorization: Bearer <token>`.

**Recent events**
: The last 2000 audit events, kept in memory for the UI. They start empty after
  a restart; full history is in your log system.

---

## 12. Audit, logs and monitoring

**Audit log**
: One line of JSON for every security-relevant event: who, what, when, from
  where, and the result. It's the answer to "who downloaded this file?".

**Audit events**

  | Event | When |
  |---|---|
  | `conn_rejected` | a connection is refused before login (IP, throttle, connection limit) |
  | `auth_failed` | a login fails |
  | `session_start` / `session_end` | a partner logs in or disconnects |
  | `upload`, `download`, `delete`, `rename`, `mkdir`, `rmdir` | file operations |
  | `action` | a post-upload job finished or failed |
  | `transfer` | a scheduled transfer moved a file or failed |
  | `config_reload` | a new config was accepted or rejected |

**Result: `ok` / `denied` / `failed`**
: Worked, refused by the rules, or broke.

**stdout / log collector**
: SFTPgate writes logs to standard output. Your platform's log system (Loki,
  Cloud Logging, ELK, Datadog) collects and keeps them.

**Retention-locked (WORM) bucket**
: Storage where logs can't be changed or deleted for a set time ("write once,
  read many"). Recommended for audit logs, for compliance.

**Metrics**
: Numbers that change over time, like logins per minute or bytes uploaded,
  shown at `/metrics`.

**Prometheus**
: The standard tool that collects metrics and checks alert rules.

**Grafana**
: The standard tool that draws metrics as dashboards.

**Dashboard**
: A page of graphs. SFTPgate ships one with 19 panels.

**Alert / alert rule**
: A condition that notifies someone, like "storage errors in the last 5
  minutes". SFTPgate ships 11 alert rules.

**Label**
: A tag on a metric, like `account="razorpay"`, used to filter and group.

**Tracing / OpenTelemetry / OTLP / span / trace**
: Detailed timing of each operation. A **trace** is one session; each upload
  or step inside it is a **span**. OpenTelemetry is the standard, and OTLP is
  its sending format. Off unless `OTEL_EXPORTER_OTLP_ENDPOINT` is set.

**Jaeger / Tempo**
: Tools that show traces.

**`traceparent`**
: A web header that carries the trace id, so the receiving system can continue
  the same trace.

**Health check / `/healthz`**
: "Is the process alive?" A yes/no answer for Docker and Kubernetes.

**Readiness / `/readyz`**
: "Is it ready to serve?" It says no while a bucket can't be reached, so no
  partner is sent to a broken copy.

---

## 13. Deployment

**Container / image**
: A packaged, ready-to-run copy of SFTPgate with everything it needs. The
  **image** is the package; a **container** is a running copy.

**Docker / Docker Compose**
: Docker runs containers on one machine. Compose starts several related
  containers together (SFTPgate, test storage, monitoring) from one file.

**Distroless**
: A minimal image with no shell or extra tools. Less inside means less to
  attack.

**Non-root / `65532`**
: SFTPgate runs as an ordinary user (id 65532), never as the all-powerful root.

**Read-only root filesystem**
: The container can't change its own files. Only its data folder and `/tmp`
  are writable.

**Volume / mount**
: Storage attached to a container, like the config folder or the spool disk.
  **Mount the config folder, not the single file:** editors and git replace
  files, and a single-file mount keeps showing the old one.

**GHCR**
: GitHub Container Registry, where the SFTPgate image is published
  (`ghcr.io/shashankpatil-dev/sftpgate`).

**Kubernetes / K8s / cluster**
: A system that runs containers across many machines and keeps them the way
  you describe: it restarts crashed ones and moves them off failed machines.

**Pod**
: The smallest unit Kubernetes runs: one SFTPgate container (plus helpers
  like git-sync).

**Helm / chart / values**
: Helm installs apps on Kubernetes. A **chart** is the installable package, and
  **values** are your settings for it (`values.yaml`).

**Manifest**
: A Kubernetes file describing what to run. `deploy/kubernetes/` has plain ones
  for clusters without Helm.

**StatefulSet**
: A Kubernetes way to run pods that each keep **their own disk** and a stable
  name (`sftpgate-0`, `sftpgate-1`). SFTPgate uses it so each pod's spool and
  queue survive restarts.

**PVC / PersistentVolumeClaim / persistence**
: A request for a disk that survives pod restarts. SFTPgate's spool and queue
  live on it. Default size 2 GiB.

**ConfigMap**
: Kubernetes storage for a plain config file. It holds `sftpgate.yaml`.

**Service / LoadBalancer / ClusterIP**
: How pods are reached. A **LoadBalancer** gets a public address (for partners);
  **ClusterIP** is reachable only inside the cluster (for metrics and the UI).

**Ingress**
: A web entry point into the cluster. Optional, for the UI.

**ServiceAccount**
: The identity a pod runs as in Kubernetes. Cloud workload identity is attached
  to it.

**ServiceMonitor / PrometheusRule**
: Settings for the Prometheus Operator: "collect metrics from SFTPgate" and
  "use these alert rules".

**PodDisruptionBudget**
: A rule that stops Kubernetes from taking down too many SFTPgate pods at once
  during maintenance.

**Probe (startup / liveness / readiness)**
: Kubernetes regularly calls `/healthz` and `/readyz`. If the process is dead,
  it's restarted; if it's not ready, no partners are sent to it.

**Replicas**
: How many copies of SFTPgate run. More copies mean more capacity and
  surviving a machine failure.

**Sidecar**
: A helper container running next to SFTPgate in the same pod, like git-sync.

**kind**
: "Kubernetes in Docker". A full Kubernetes cluster on your laptop, for testing.

**Graceful shutdown**
: On stop, SFTPgate waits up to 30 seconds for uploads in progress to finish.
  An upload cut off is never saved halfway.
