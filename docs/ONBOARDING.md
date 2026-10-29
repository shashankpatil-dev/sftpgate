# Onboarding: running SFTPgate in your organization

This guide takes you from nothing to a running SFTPgate that partners can use,
with **Docker** (one server) or **Helm** (Kubernetes). The second half is a
reference for **every config setting**.

Not sure what a word means? See [GLOSSARY.md](GLOSSARY.md).
What you get at the end: [WHAT-YOU-GET.md](WHAT-YOU-GET.md).

**Contents**

1. [Before you start](#1-before-you-start)
2. [The pieces you'll create](#2-the-pieces-youll-create)
3. [Option A: Docker](#3-option-a-docker)
4. [Option B: Kubernetes with Helm](#4-option-b-kubernetes-with-helm)
5. [Adding a partner](#5-adding-a-partner-the-everyday-task)
6. [Secrets, passwords and tokens](#6-secrets-passwords-and-tokens)
7. [Giving SFTPgate access to your buckets](#7-giving-sftpgate-access-to-your-buckets)
8. [Config reference: every setting](#8-config-reference-every-setting)
9. [Environment variables](#9-environment-variables)
10. [Helm values reference](#10-helm-values-reference)
11. [Checking that it works](#11-checking-that-it-works)
12. [Common problems](#12-common-problems)

---

## 1. Before you start

You need:

- **A bucket.** GCS, S3 (or MinIO, R2, Ceph) or Azure Blob. Local disk works for
  trying things out.
- **Somewhere to run it.**
  - **Docker:** any Linux server with Docker.
  - **Kubernetes:** any cluster (GKE, EKS, AKS, OpenShift, on-premise) plus Helm 3.
- **A git repository** for your config. This is recommended: every change is
  reviewed and recorded.
- **`ssh-keygen`.** It comes with OpenSSH on Linux and macOS, and Git for
  Windows includes it.
- **The `sftpgate` binary on your laptop,** for `validate` and `hash-password`.
  Build it with `make build`, or run it from the container image:
  `docker run --rm ghcr.io/shashankpatil-dev/sftpgate:0.3.0 version`.

> **About the image:** `ghcr.io/shashankpatil-dev/sftpgate:<version>` is
> published when a version tag (like `v0.3.0`) is pushed. Until then, build it
> yourself from the repository and use that name instead:
> `docker build -t sftpgate:local .`

---

## 2. The pieces you'll create

Whatever you run it on, you make the same four things:

| Piece | What it is | Where it lives |
|---|---|---|
| **Host key** | The server's identity. Make it once and keep it forever. | A secret (file) |
| **Config** (`sftpgate.yaml`) | Accounts, folders, storage, rules | Git, then a mounted folder or ConfigMap |
| **Secrets** | Password hashes, PGP keys, tokens | Env vars, secret files, or a cloud secret store |
| **Bucket access** | Permission to use your buckets | The cloud's identity system, never the config |

### Make the host key (once)

```sh
ssh-keygen -t ed25519 -N "" -f ssh_host_ed25519_key
```

This creates `ssh_host_ed25519_key` (private, keep it secret) and
`ssh_host_ed25519_key.pub`. Keep a backup. If you lose it or change it, every
partner sees a "host key changed" warning that looks like an attack.

Get the fingerprint to send to partners:

```sh
ssh-keygen -lf ssh_host_ed25519_key.pub
# 256 SHA256:tqtLs848VAa/Uuq/Z7AHUvysVTVqJ8V9t8dgY02fG+M (ED25519)
```

### Write a first config

Start small: one storage, one test account.

```yaml
version: 1

server:
  host_keys: [/etc/sftpgate/keys/ssh_host_ed25519_key]
  timezone: Asia/Kolkata

storages:
  main: "gs://acme-sftp-exchange"          # your bucket

accounts:
  - name: test-partner
    description: Our own test login
    auth:
      public_keys:
        - "ssh-ed25519 AAAAC3Nza...your-public-key... you@laptop"
    folders:
      - path: /inbound
        storage: main
        prefix: test-partner/inbound/
        perms: [list, upload]
      - path: /outbound
        storage: main
        prefix: test-partner/outbound/
        perms: [list, download]
```

Check it before going further:

```sh
sftpgate validate -offline sftpgate.yaml
```

---

## 3. Option A: Docker

Best for one server, a small team, or trying it out.

### Step 1: lay out a folder on the server

```
/opt/sftpgate/
├── config/
│   └── sftpgate.yaml          # your config (or a git checkout, see step 5)
├── keys/
│   └── ssh_host_ed25519_key   # the host key
└── secrets/                   # files for ${file:...} references (optional)
```

The container runs as user 65532, so it must be able to read these files:

```sh
sudo chown -R 65532:65532 /opt/sftpgate/keys /opt/sftpgate/secrets
sudo chmod 600 /opt/sftpgate/keys/ssh_host_ed25519_key
```

### Step 2: run it

```sh
docker run -d --name sftpgate --restart unless-stopped \
  --read-only --tmpfs /tmp \
  -p 22:2222 -p 127.0.0.1:8080:8080 \
  -v /opt/sftpgate/config:/etc/sftpgate/config:ro \
  -v /opt/sftpgate/keys:/etc/sftpgate/keys:ro \
  -v /opt/sftpgate/secrets:/etc/sftpgate/secrets:ro \
  -v sftpgate-data:/var/lib/sftpgate \
  -e SFTPGATE_CONFIG=/etc/sftpgate/config/sftpgate.yaml \
  --env-file /opt/sftpgate/secrets.env \
  ghcr.io/shashankpatil-dev/sftpgate:0.3.0
```

What each part does:

| Part | Why |
|---|---|
| `-p 22:2222` | Partners connect on the normal SFTP port 22. If the host's own SSH already uses 22, use another port, like `-p 2222:2222`. |
| `-p 127.0.0.1:8080:8080` | Metrics, health checks and the API, on the server itself only. Put a firewall or VPN in front before opening it further. |
| `-v .../config:...:ro` | The config **folder**, read-only. A folder, not the file, so edits are seen (hot reload). |
| `-v .../keys:...:ro` | The host key. |
| `-v sftpgate-data:/var/lib/sftpgate` | The spool and the action queue. Keep it, or queued work is lost. |
| `--read-only --tmpfs /tmp` | Lock the container's own files; give it a scratch `/tmp`. |
| `--env-file secrets.env` | Secrets for `${env:...}` references, one `NAME=value` per line, file readable by root only. |

Or use Compose: put the same settings in a `docker-compose.yml`. The repo's
[`deploy/compose/docker-compose.yml`](../deploy/compose/docker-compose.yml) is a
working example, which also starts Prometheus, Grafana and Jaeger.

### Step 3: check it's healthy

```sh
docker ps                      # STATUS shows (healthy)
docker logs sftpgate | head    # "sftpgate started"
curl -s localhost:8080/readyz  # ok
```

### Step 4: connect

```sh
sftp -i your_private_key test-partner@your-server
```

### Step 5 (recommended): run the config from git

Pick one:

- **Simplest:** `git pull` in `/opt/sftpgate/config` from a cron job or
  your deploy pipeline. SFTPgate notices the change within 10 seconds.
- **Automatic:** use git-sync as in
  [`deploy/compose/gitops.yml`](../deploy/compose/gitops.yml):

  ```sh
  GITOPS_REPO=https://github.com/acme/sftpgate-config.git \
  GITOPS_PATH=prod/sftpgate.yaml \
  docker compose -f docker-compose.yml -f gitops.yml up -d
  ```

### Updating SFTPgate

```sh
docker pull ghcr.io/shashankpatil-dev/sftpgate:<new-version>
docker stop sftpgate     # waits up to 30s for uploads to finish
docker rm sftpgate
# run the same docker run command with the new version
```

---

## 4. Option B: Kubernetes with Helm

Best when partners depend on it: it restarts itself, survives machine failures,
can run several copies, and plugs into cluster monitoring.

### Step 1: namespace and host key

```sh
kubectl create namespace sftpgate
kubectl -n sftpgate create secret generic sftpgate-host-keys \
  --from-file=ssh_host_ed25519_key
```

### Step 2: secrets

Make one Secret for values used as `${env:NAME}`:

```sh
kubectl -n sftpgate create secret generic sftpgate-secrets \
  --from-literal=RAZORPAY_PW_HASH='$2a$12$...' \
  --from-literal=SFTPGATE_API_TOKEN="$(openssl rand -hex 24)"
```

And Secrets for files used as `${file:...}`, like PGP keys:

```sh
kubectl -n sftpgate create secret generic recon-pgp \
  --from-file=private.asc --from-file=razorpay-signing.pub.asc
```

In a GitHub Actions deploy job, the same commands read from GitHub secrets:
`--from-literal=RAZORPAY_PW_HASH='${{ secrets.RAZORPAY_PW_HASH }}'`. With
External Secrets Operator, Vault or a cloud secret manager, create the same
Secrets with those tools instead.

### Step 3: your values file

`my-values.yaml`:

```yaml
replicas: 2

# The config. With gitSync (below) this part is not used.
config: |
  version: 1
  server:
    host_keys: [/etc/sftpgate/keys/ssh_host_ed25519_key]
    timezone: Asia/Kolkata
  http:
    tokens: ["${env:SFTPGATE_API_TOKEN}"]
  storages:
    main: "gs://acme-sftp-exchange"
  actions:
    decrypt-razorpay:
      pgp_decrypt:
        key: "${file:/etc/sftpgate/secrets/recon/private.asc}"
        signer_keys: ["${file:/etc/sftpgate/secrets/recon/razorpay-signing.pub.asc}"]
  accounts:
    - name: razorpay
      auth:
        mode: all
        public_keys: ["ssh-ed25519 AAAA... razorpay"]
        password_hash: "${env:RAZORPAY_PW_HASH}"
      allow_ips: ["52.66.0.0/16"]
      folders:
        - path: /inbound
          storage: main
          prefix: razorpay/inbound/
          perms: [list, upload]
          hooks:
            after_upload: [decrypt-razorpay]

# Secrets as env vars, for ${env:...}
secretEnv: [sftpgate-secrets]

# Secrets as files, for ${file:...}
secretFiles:
  - secretName: recon-pgp
    mountPath: /etc/sftpgate/secrets/recon

# Bucket access without keys (pick your cloud, see section 7)
serviceAccount:
  annotations:
    iam.gke.io/gcp-service-account: sftpgate@acme-prod.iam.gserviceaccount.com

service:
  sftp:
    # Only these networks can even reach the port (cloud firewall).
    loadBalancerSourceRanges: ["52.66.0.0/16", "203.0.113.0/24"]

persistence:
  size: 5Gi

metrics:
  serviceMonitor: { enabled: true }
  prometheusRule: { enabled: true }
  grafanaDashboard: { enabled: true }
```

### Step 4: install

```sh
helm install sftpgate deploy/helm/sftpgate -n sftpgate -f my-values.yaml
kubectl -n sftpgate rollout status statefulset/sftpgate
kubectl -n sftpgate get service sftpgate        # EXTERNAL-IP is the partner address
```

Point a DNS name like `sftp.acme.com` at that address and send partners the
name, the port (22), and the host key fingerprint (the install notes print the
command).

### Step 5: changes

- **Config change:** edit `my-values.yaml`, then
  `helm upgrade sftpgate deploy/helm/sftpgate -n sftpgate -f my-values.yaml`.
  It's picked up within about a minute **without a restart**.
- **Secret change:** update the Secret. File secrets are picked up by themselves;
  for env secrets, run `kubectl -n sftpgate rollout restart statefulset/sftpgate`.
- **New host key:** update the Secret, then restart as above (and warn partners).
- **New SFTPgate version:** `helm upgrade ... --set image.tag=<version>`. Pods
  are replaced one by one, and uploads in progress get 30 seconds to finish.

### Step 6 (recommended): config from git

Set in your values:

```yaml
gitSync:
  enabled: true
  repo: https://github.com/acme/sftpgate-config.git
  ref: main
  path: prod/sftpgate.yaml
```

Now a merged pull request goes live within 30 seconds. For a private repository,
see `gitSync.extraEnv` in section 10.

### No Helm?

Use [`deploy/kubernetes/`](../deploy/kubernetes): the same setup as plain files,
with instructions.

---

## 5. Adding a partner (the everyday task)

1. **Ask the partner for:**
   - their **public key** (one line starting with `ssh-ed25519` or `ssh-rsa`);
   - their **IP addresses** or ranges;
   - for 2-factor, a password. Hash it right away:
     `echo 'their-password' | sftpgate hash-password`.
   - Never ask for or accept their private key.

2. **Add the account** to `sftpgate.yaml` in a pull request:

   ```yaml
   - name: newvendor
     description: Newvendor daily stock files
     auth:
       public_keys: ["ssh-ed25519 AAAA... newvendor-prod"]
     allow_ips: ["198.51.100.0/24"]
     max_sessions: 3
     folders:
       - path: /inbound
         storage: main
         prefix: newvendor/inbound/
         perms: [list, upload]
         patterns: ["*.csv"]
         max_file_size: 200MiB
   ```

3. **If you used a password,** add its hash to your secret store as
   `NEWVENDOR_PW_HASH` and reference it: `password_hash: "${env:NEWVENDOR_PW_HASH}"`.

4. **CI runs** `sftpgate validate -offline`; a reviewer approves; merge.

5. **Send the partner:** the address, the port, their username (`newvendor`), and
   the host key fingerprint.

6. **Check** `GET /api/v1/events?account=<name>` or the audit log for their first `session_start`
   and `upload`.

**Removing a partner:** delete their account block and merge. Open sessions are
closed on the next reload.

---

## 6. Secrets, passwords and tokens

### The one rule

**No secret values in `sftpgate.yaml`.** Secret fields must be a reference;
`validate` refuses anything else. Public keys are not secret and may be written
directly.

| Setting | Secret? | How to give it |
|---|---|---|
| `auth.public_keys` | No | Directly, or a reference |
| `auth.password_hash` | **Yes** (a leaked hash can be cracked offline) | Reference only |
| `server.host_keys` | **Yes** | A file path (mounted secret) |
| `pgp_decrypt.key`, `pgp_decrypt.passphrase` | **Yes** | Reference only |
| `pgp_decrypt.signer_keys`, `pgp_encrypt.recipients` | No (public keys) | Directly, or a reference |
| `webhook.secret` | **Yes** | Reference only |
| `http.tokens` | **Yes** (at least 16 characters) | Reference only |
| `remotes.*.private_key`, `remotes.*.password` | **Yes** | Reference only |
| `remotes.*.host_key` | No | Directly, or a reference |

### The three kinds of reference

```yaml
password_hash: "${env:RAZORPAY_PW_HASH}"                     # environment variable
key: "${file:/etc/sftpgate/secrets/recon/private.asc}"        # a file
password_hash: "${secret:gcpsecretmanager://projects/acme/secrets/razorpay-pw-hash}"
password_hash: "${secret:awssecretsmanager://razorpay-pw-hash?region=ap-south-1}"
password_hash: "${secret:awsparamstore://razorpay-pw-hash?region=ap-south-1}"
```

- **`env`:** simplest. Docker: `--env-file`. Helm: `secretEnv`.
- **`file`:** best for keys (multi-line). Docker: mount a folder. Helm:
  `secretFiles`. Rotations are picked up by themselves.
- **`secret`:** reads straight from a cloud secret store, using the same cloud
  identity as the buckets. After rotating, send `SIGHUP` or restart.

### Partner passwords

```sh
sftpgate hash-password            # asks twice, prints $2a$12$...
echo 'pw' | sftpgate hash-password -cost 12
```

Store the **hash** in your secret store and reference it. The partner keeps the
password.

### API and admin tokens

```sh
openssl rand -hex 24              # a strong token
```

Store it as a secret and list it under `http.tokens`, then send
`Authorization: Bearer <token>`. Several tokens are allowed (for example one per
team), so you can change one without cutting everyone off.

Admin tokens (`http.admin.tokens`) are made the same way but kept separate:
they must differ from every read-only token, and only work as a Bearer header.
Give them only to the people or jobs that must run a transfer now or retry
failed actions, and list only those operations in `http.admin.allow`.

---

## 7. Giving SFTPgate access to your buckets

Credentials never go in the config. Give the SFTPgate process an identity
that's allowed to use the buckets, and nothing more.

**Permissions it needs** on each bucket: read, write, list and delete objects.

| Cloud | Kubernetes (recommended) | VM or Docker |
|---|---|---|
| **GCS** | Workload Identity: annotate the ServiceAccount with `iam.gke.io/gcp-service-account: <gsa>@<project>.iam.gserviceaccount.com`, and give that account **Storage Object Admin** on the bucket. | The VM's service account, or `GOOGLE_APPLICATION_CREDENTIALS=/path/key.json` |
| **AWS S3** | IRSA: annotate the ServiceAccount with `eks.amazonaws.com/role-arn: arn:aws:iam::<acct>:role/sftpgate`, with an IAM policy allowing `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject` and `s3:ListBucket` on the bucket. | The instance role, or `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` |
| **Azure Blob** | Workload Identity: annotate the ServiceAccount with `azure.workload.identity/client-id: <id>`, set `AZURE_STORAGE_ACCOUNT`, and grant **Storage Blob Data Contributor**. | `AZURE_STORAGE_ACCOUNT` plus `AZURE_STORAGE_KEY`, or a managed identity |
| **MinIO / Ceph / R2** | `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` from a Secret (`secretEnv`) | the same variables |

Storage URL examples:

```yaml
storages:
  gcs-bucket:   "gs://acme-recon-raw"
  s3-bucket:    "s3://acme-reports?region=ap-south-1"
  minio-bucket: "s3://exchange?endpoint=https://minio.internal:9000&use_path_style=true&region=us-east-1"
  azure:        "azblob://exchange"          # with AZURE_STORAGE_ACCOUNT set
  local:        "file:///var/lib/sftpgate/data"
```

Use a **separate bucket, or at least a separate prefix, per partner or purpose**,
and keep SFTPgate's permissions limited to those buckets.

---

## 8. Config reference: every setting

Every setting, what it does, its default, and when to change it. Settings you
leave out take their default.

### 8.1 Top level

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

### 8.2 `server`

| Setting | Default | What it does | When to change |
|---|---|---|---|
| `listen` | `":2222"` | Address and port for SFTP inside the container | Rarely; map the port outside instead |
| `host_keys` | (required) | Paths to the server's private host key files | Always set |
| `allow_ips` | empty (any) | IPs or ranges allowed to connect at all, checked first | To lock the whole server to known networks |
| `banner` | empty | Message shown before login | For a legal notice |
| `idle_timeout` | `15m` | Close connections idle this long | Lower for stricter security |
| `max_auth_tries` | `6` | Login attempts per connection | Rarely |
| `max_connections` | `25` | Open connections, all accounts together | Raise for busy servers; about 4× your normal peak is a good value |
| `spool_dir` | `/var/lib/sftpgate/spool` | Where uploads wait until complete | Only outside the official image |
| `min_spool_free` | `1GiB` | Free space always kept on the spool disk | Lower on small disks |
| `reload_interval` | `10s` | How often to look for config changes | Rarely |
| `timezone` | `UTC` | Time zone for `{date}`, `{time}` and schedules | Set to your zone, like `Asia/Kolkata` |
| `throttle.max_failures` | `5` | Failed logins from one IP before blocking | — |
| `throttle.window` | `10m` | The period those failures are counted over | — |
| `throttle.block` | `15m` | How long the IP is then blocked | — |

Durations are written like `30s`, `15m`, `2h`. Sizes are written like `512KiB`,
`500MiB`, `2GiB`.

### 8.3 `http`

| Setting | Default | What it does |
|---|---|---|
| `listen` | `":8080"` | Address for `/metrics`, `/healthz`, `/readyz` and `/api/v1/` |
| `tokens` | empty (API off) | Tokens that open the read-only API. References only, 16+ characters. |
| `admin.tokens` | empty (admin off) | Tokens for the admin calls. References only, 16+ characters, different from `tokens`. |
| `admin.allow` | empty (admin off) | The admin calls that exist: `run_transfer` (`POST /api/v1/transfers/<name>/run`) and `retry_actions` (`POST /api/v1/actions/retry[?job=<id>]`). Anything not listed answers 404. Needs `admin.tokens`, and tokens need at least one entry here. |

Run-transfer answers `202` and runs in the background; the result is the
`transfer` audit event and metrics. It answers `409` while that transfer is
running or on a replica that doesn't run transfers (see
`SFTPGATE_TRANSFERS_ONLY_ON`). Retry works on the failed jobs of the pod you
reach, since each pod keeps its own queue. Every admin call is an `admin`
audit event.

### 8.4 `storages`

A map of **short name → bucket URL** (see section 7). Folders, copies and
transfers refer to storages by these names.

```yaml
storages:
  recon-raw: "gs://acme-recon-raw"
  reports:   "s3://acme-reports?region=ap-south-1"
```

Names use lowercase letters, numbers and `-`.

### 8.5 `accounts`

A list. Each item is one login.

| Setting | Default | What it does |
|---|---|---|
| `name` | (required) | The SFTP username. Lowercase letters, numbers, `.`, `_`, `-`. |
| `description` | empty | A note for people, shown nowhere to partners |
| `auth.mode` | `any` | `any`: key **or** password. `all`: key **and** password. |
| `auth.public_keys` | empty | The partner's public keys, one line each |
| `auth.password_hash` | empty | Reference to a bcrypt hash |
| `allow_ips` | empty (any) | IPs or ranges this account may log in from |
| `max_sessions` | `0` (no limit) | Connections this account may have open at once |
| `folders` | (at least one) | What the account sees, see below |

`mode: all` needs both `public_keys` and `password_hash`. `mode: any` needs at
least one of them.

**Client support for `mode: all` (key and password):** OpenSSH, PuTTY (and so
WinSCP and FileZilla) and Go clients work. **Paramiko (Python) can't do the
two-step login** against SFTPgate: it repeats a protocol message that Go's SSH
library rejects. Give Paramiko partners `mode: any` with a key instead.

### 8.6 `accounts[].folders`

| Setting | Default | What it does |
|---|---|---|
| `path` | (required) | The folder name the partner sees, one level, like `/inbound` |
| `storage` | (required) | Which storage the files live in |
| `prefix` | empty | The place inside the bucket, like `razorpay/inbound/` |
| `perms` | (required) | Any of `list`, `download`, `upload`, `overwrite`, `delete`, `rename`, `mkdir` |
| `patterns` | empty (any name) | Allowed file names, like `["*.csv", "*.csv.pgp"]` |
| `max_file_size` | `0` (no limit) | Largest file allowed; also the space reserved per upload |
| `hooks` | empty | Actions to run, by name: `before_upload`, `after_upload`, `after_download`. See 8.7 |
| `on_upload` | empty | The older way to write `hooks.after_upload`, with the steps in place. Still works; not together with `hooks.after_upload`. |

Typical folder setups:

```yaml
# Partner sends files to us
- { path: /inbound,  storage: main, prefix: p/in/,  perms: [list, upload] }
# Partner fetches files we prepared
- { path: /outbound, storage: main, prefix: p/out/, perms: [list, download, delete] }
# Partner may also fix a wrong upload
- { path: /drop,     storage: main, prefix: p/drop/, perms: [list, upload, overwrite, rename] }
```

### 8.7 `actions` and `hooks`

**Actions** are defined once, by name, under the top-level `actions:`. Each is
exactly **one** kind. **Hooks** on folders and transfers then list action names,
which run in that order. The original file is never changed: actions work on a
copy and write new files.

```yaml
actions:
  recon-csv: { check: { csv: { header: [id, amount], min_rows: 1 } } }
  notify:    { webhook: { url: "https://recon.internal/hooks/sftpgate", secret: "${env:RECON_HOOK_SECRET}" } }

accounts:
  - name: razorpay
    folders:
      - path: /inbound
        storage: main
        perms: [list, upload]
        hooks:
          before_upload: [recon-csv]
          after_upload: [notify]
```

**The hooks:**

| Hook | On | Runs | When an action fails |
|---|---|---|---|
| `before_upload` | folder | `check` only, while the partner waits | The upload is refused, nothing is stored. The reason goes to the partner's client and the audit log (`result: denied`). |
| `after_upload` | folder | any action, in the background | Retried; after `max_attempts` the job moves to `queue/failed` |
| `after_download` | folder | any action, once the partner has the whole file | Retried |
| `before_push` | push transfer | `check`, `pgp_encrypt`. What's left is what gets sent. | The file isn't sent, stays in place, and the run fails (alert) |
| `after_push` | push transfer | any action (only `webhook` with `after: delete`, since the file is gone) | Retried |
| `before_pull` | pull transfer | `check` only | The file isn't stored, isn't deleted on their side, and the run fails |
| `after_pull` | pull transfer | any action | Retried |

"Before" hooks only check (or, for a push, encrypt), because something is
waiting on them. A failed `check` in an `after_*` hook gives up at once: retrying
can't fix the file, and the actions after it don't run.

**What a partner sees when a check refuses a file.** SFTP can only refuse a file
when the client closes it, and most clients don't show the server's message
then:

| Client | Shows |
|---|---|
| Go `pkg/sftp` | the reason, e.g. `upload rejected: CSV header is ["name"], expected ["id" "amount"]` |
| OpenSSH `sftp` | `Failure` |
| PuTTY `psftp` (the engine of WinSCP and FileZilla) | `error while closing: failure` |
| Paramiko | ignores errors on close; with its default `confirm=True` the upload then fails with "no such file" |

The file is never stored either way, and the audit log always has the reason
(`event: upload`, `result: denied`). When you onboard a partner, tell them that
a refused upload means "fix the file and send it again", and ask Paramiko users
to keep `confirm=True` (the default). The same holds for any upload refused at
close, such as a storage error.

#### The kinds of action

**`check`:** reject a file that doesn't look right. It only reads the file. At
least one rule:

| Setting | What it does |
|---|---|
| `min_size`, `max_size` | Size limits, like `100` or `5MiB` |
| `csv.header` | The first row must be exactly these columns. A spreadsheet byte-order mark is ignored. |
| `csv.min_rows` | At least this many rows after the first one |
| `csv.delimiter` | One character; default `,` |

With `csv`, every row must have as many columns as the first one.

**`pgp_decrypt`:** decrypt with your private key.

| Setting | What it does |
|---|---|
| `key` | **Reference** to your armored PGP private key |
| `passphrase` | **Reference** to the key's passphrase, if it has one |
| `signer_keys` | Partner public keys. When set, files must be signed by one of them. |

**`pgp_encrypt`:** encrypt for recipients.

| Setting | What it does |
|---|---|
| `recipients` | Armored public keys of who may read the file. Adds `.pgp` to the name. A key that can't encrypt (sign-only, expired) is refused when the config loads. |

**`copy`:** save the current file to a storage.

| Setting | What it does |
|---|---|
| `storage` | Target storage name |
| `key` | Target name, with placeholders: `{account}`, `{folder}`, `{transfer}`, `{date}`, `{time}`, `{filename}`, `{job_id}` |

**`webhook`:** tell another system.

| Setting | What it does |
|---|---|
| `url` | `https://...` (or `http://` inside your network) |
| `secret` | **Reference** to a shared secret; the body is signed in `X-Sftpgate-Signature: sha256=<hex>` |
| `headers` | Extra headers, for a receiver with its own auth. Headers whose name contains auth, token, key, secret or cookie must be references. `Content-Type`, `User-Agent` and `X-Sftpgate-*` can't be set. |

A hook with only webhooks doesn't download the file; it checks the SHA-256
stored with it instead, so a replaced file is never announced.

What the webhook receives (POST, JSON):

```json
{
  "event": "upload",
  "job_id": "20261024T075812Z-1a2b3c4d",
  "account": "razorpay",
  "folder": "/inbound",
  "storage": "recon-raw",
  "key": "razorpay/inbound/settlement.csv.pgp",
  "filename": "settlement.csv.pgp",
  "size": 10482,
  "sha256": "ab12...",
  "time": "2026-10-24T07:58:12Z",
  "uploaded": "2026-10-24T07:58:12Z",
  "copies": [{ "storage": "warehouse", "key": "razorpay/2026-10-24/settlement.csv" }]
}
```

- **`event`** is `upload`, `download`, `push` or `pull`.
- For transfers, `transfer`, `remote` and `remote_path` are set, and `account`
  and `folder` are empty. For a push, `key` is where the file is now (for
  example in `sent/`) and `remote_path` is the name the partner got.
- `uploaded` holds the same time as `time`, for receivers written for v0.3.
- `sha256` can be empty for a pushed file that no action read.

To check the signature, compute HMAC-SHA256 of the raw body with the secret and
compare it with the header. Ignore a `job_id` you've already handled, because a
retry can send it twice. Answer with any 2xx status within 10 seconds; anything
else is retried.

### 8.8 `server.queue`

How the actions of `after_*` hooks run in the background.

| Setting | Default | What it does |
|---|---|---|
| `workers` | `4` | Jobs running at the same time |
| `max_attempts` | `8` | Tries before a job gives up and moves to `queue/failed` |
| `backoff` | `30s` | First wait before a retry; doubles each time, up to 1 hour |

Up to v0.3 these were under a top-level `actions:`. An old file now gets a clear
error saying where each setting moved.

### 8.9 `remotes`

A map of **short name → partner SFTP server**, used by transfers.

| Setting | What it does |
|---|---|
| `addr` | `host:port`, like `sftp.bank.example:22` |
| `user` | The username **you** were given on their server |
| `host_key` | **Their** server's public host key, pinned. Ask them, or run `ssh-keyscan -t ed25519 sftp.bank.example`, then confirm the fingerprint with them. |
| `private_key` | **Reference** to your private key for their server |
| `password` | **Reference** to your password for their server |

At least one of `private_key` or `password` is needed. Give the partner your
**public** key, made with `ssh-keygen -t ed25519 -f bank_key`.

### 8.10 `transfers`

| Setting | What it does |
|---|---|
| `name` | A unique name, lowercase with `-` |
| `schedule` | Cron: `"0 6 * * *"` daily at 06:00, `"*/30 * * * *"` every 30 min, `"@every 10m"` |
| `pull` **or** `push` | What to do, see below |

**`pull`:** fetch from the partner into a bucket.

| Setting | What it does |
|---|---|
| `from.remote` | Which remote |
| `from.path` | Folder on their server, like `/out` |
| `from.pattern` | Which files, like `"*.csv"` (empty = all) |
| `to.storage` | Target storage |
| `to.key` | Target name; must contain `{filename}`. Placeholders: `{name}`, `{date}`, `{time}`, `{filename}`. |
| `delete_after` | `true` = remove their copy once stored. Default `false`. |

**`push`:** send from a bucket to the partner.

| Setting | What it does |
|---|---|
| `from.storage` | Source storage |
| `from.prefix` | Files directly under this prefix, like `payouts/outbox/` |
| `from.pattern` | Which files (empty = all) |
| `to.remote` | Which remote |
| `to.path` | Folder on their server, like `/in` |
| `after` | `keep` (default; skips files they already have), `delete`, or `move:<prefix>` |

---

## 9. Environment variables

| Variable | What it does |
|---|---|
| `SFTPGATE_CONFIG` | Path of the config file. Default `/etc/sftpgate/sftpgate.yaml`. |
| `SFTPGATE_REVISION` | The git commit to show in the API and metrics. Found automatically with git-sync. |
| `SFTPGATE_TRANSFERS` | `off` disables scheduled transfers on this copy |
| `SFTPGATE_TRANSFERS_ONLY_ON` | Run transfers only on the copy with this host name (Helm sets it) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Turns tracing on, like `http://otel-collector:4318` |
| `OTEL_*` | Any other standard OpenTelemetry setting |
| `AWS_*`, `GOOGLE_APPLICATION_CREDENTIALS`, `AZURE_STORAGE_*` | Cloud credentials, when not using workload identity |
| *your own names* | Values for `${env:NAME}` references |

---

## 10. Helm values reference

| Value | Default | What it does |
|---|---|---|
| `image.repository` / `image.tag` / `image.pullPolicy` | `ghcr.io/shashankpatil-dev/sftpgate` / chart version / `IfNotPresent` | Which image to run |
| `imagePullSecrets` | `[]` | For a private registry |
| `replicas` | `1` | How many copies. With more than 1, transfers run on the first only. Ignored when autoscaling is on. |
| `autoscaling.enabled` / `minReplicas` / `maxReplicas` / `targetCPUUtilizationPercentage` | off / `2` / `4` / `70` | A HorizontalPodAutoscaler on CPU. Transfers still run on one pod. It scales down slowly (one pod per 5 min), because removing a pod disconnects its partners. |
| `config` | a minimal config | The full `sftpgate.yaml` |
| `existingConfigMap` | empty | Use your own ConfigMap (key `sftpgate.yaml`) instead of `config` |
| `hostKeys.existingSecret` | `sftpgate-host-keys` | Secret with the host key file(s) |
| `secretEnv` | `[]` | Secret names loaded as env vars (`${env:}`) |
| `secretFiles` | `[]` | `{secretName, mountPath}` pairs mounted as files (`${file:}`) |
| `env` | `[]` | Extra env vars (tracing, cloud settings) |
| `service.sftp.type` | `LoadBalancer` | How partners reach it (`NodePort`, `ClusterIP` also possible) |
| `service.sftp.port` | `22` | The public port |
| `service.sftp.externalTrafficPolicy` | `Local` | Keeps partners' real IPs for allowlists |
| `service.sftp.loadBalancerSourceRanges` | `[]` | Cloud firewall: who may reach the port at all |
| `service.sftp.annotations` | `{}` | Cloud load balancer options (internal LB, static IP and so on) |
| `service.http.port` / `annotations` | `8080` / `{}` | The internal metrics, health and API service |
| `persistence.enabled` / `size` / `storageClass` / `accessModes` | `true` / `2Gi` / default / `[ReadWriteOnce]` | Disk per pod for the spool and queue |
| `gitSync.enabled` / `repo` / `ref` / `path` / `period` | off / – / `main` / `sftpgate.yaml` / `30s` | Config from git |
| `gitSync.extraEnv` | `[]` | git-sync credentials for private repositories |
| `gitSync.image` | `registry.k8s.io/git-sync/git-sync:v4.4.2` | git-sync version |
| `serviceAccount.create` / `name` / `annotations` | `true` / – / `{}` | Pod identity; workload identity annotations go here |
| `metrics.serviceMonitor.enabled` / `interval` / `labels` | off / `30s` / `{}` | Prometheus Operator scraping |
| `metrics.prometheusRule.enabled` / `labels` | off / `{}` | The 11 alert rules |
| `metrics.grafanaDashboard.enabled` / `labels` | off / `grafana_dashboard: "1"` | Dashboard for the Grafana sidecar |
| `ingress.enabled` / `className` / `host` / `annotations` / `tls` | off | Web access to the API (and admin calls, if allowed) |
| `podDisruptionBudget.enabled` / `minAvailable` | off / `1` | Keep pods up during cluster maintenance |
| `resources` | 100m CPU, 128Mi requested, 512Mi limit | Pod size |
| `terminationGracePeriodSeconds` | `45` | Time to finish uploads on shutdown |
| `podAnnotations`, `podLabels`, `nodeSelector`, `tolerations`, `affinity` | empty | Standard pod placement settings |
| `topologySpreadConstraints`, `priorityClassName` | empty | Spread pods across zones; give SFTPgate scheduling priority |
| `extraVolumes`, `extraVolumeMounts` | empty | Mount anything else, for example a corporate CA bundle for a private S3 endpoint |

Private git repository with a token:

```yaml
gitSync:
  extraEnv:
    - name: GITSYNC_USERNAME
      value: sftpgate-bot
    - name: GITSYNC_PASSWORD
      valueFrom: { secretKeyRef: { name: git-creds, key: token } }
```

---

## 11. Checking that it works

| Check | How |
|---|---|
| Config is valid | `sftpgate validate -offline sftpgate.yaml` |
| Process is alive | `curl localhost:8080/healthz` (Docker: `docker ps` shows healthy) |
| Buckets reachable | `curl localhost:8080/readyz` |
| A partner can log in | `sftp -i key -P 22 account@host` |
| What's happening | Grafana, `GET /api/v1/events`, or `docker logs` / `kubectl logs` |
| Which config is live | `curl -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/status` |
| Metrics | `curl localhost:8080/metrics` |

---

## 12. Common problems

| You see | Likely cause | Fix |
|---|---|---|
| `inline secret not allowed` | A secret is written into the config | Use `${env:}`, `${file:}` or `${secret:}` |
| `env X not set` | The reference points to a missing variable | Add it to `--env-file` / `secretEnv` |
| `possible plaintext password` | `password_hash` isn't a bcrypt hash | Use `sftpgate hash-password` |
| Partner: "Permission denied (publickey)" | Wrong key, wrong username, or IP not allowed | Check the audit log: `auth_failed` shows the user tried |
| Partner: connection closes at once | IP blocked by the allowlist or throttle | Look for `conn_rejected` and its reason |
| Partner: "Host key verification failed" | The host key changed, or they saved the wrong one | Restore the original key; send the fingerprint again |
| Upload refused: "out of upload space" | Spool disk too small for `max_file_size` × uploads at once | Increase `persistence.size` or lower `max_file_size` |
| Upload refused: "file already exists" | The folder has no `overwrite` | Add `overwrite`, or have the partner use a new name |
| Edits aren't picked up (Docker) | The config **file** is mounted instead of the folder | Mount the folder |
| `/readyz` fails | A bucket can't be reached, or there's no permission | Check bucket access (section 7) |
| Very old partner software can't connect | It only supports SHA-1 | The partner must upgrade; SHA-1 is never allowed |
