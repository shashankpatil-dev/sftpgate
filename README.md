# SFTPgate

A secure SFTP gateway for exchanging files with vendors, partners and internal
services, configured from **one YAML file in git**.

SFTPgate runs as a single container. Partners connect with any normal SFTP
client. Files go straight into your buckets (S3, GCS, Azure Blob or any
S3-compatible store), and every access is logged.

- **GitOps:** accounts, keys, IP rules, folders and permissions live in git.
  Onboarding a vendor is a pull request; offboarding is deleting lines.
- **Locked down:** SFTP only. No shell, no commands, no port forwarding.
- **No half files:** an upload appears in the bucket only once it is complete,
  with its SHA-256 stored alongside it.
- **Audited:** one JSON log line for every login and file operation.
- **Nothing extra to run:** no database. One binary, one config file.

## Documentation

| Read this | When you want to |
|---|---|
| [docs/ONBOARDING.md](docs/ONBOARDING.md) | set it up with Docker or Helm, add partners, and look up every config setting |
| [docs/WHAT-YOU-GET.md](docs/WHAT-YOU-GET.md) | see what it gives partners, your teams, security and operations |
| [docs/GLOSSARY.md](docs/GLOSSARY.md) | understand a term (spool, queue, prefix, host key, ...) in plain words |
| [docs/LOAD-TEST.md](docs/LOAD-TEST.md) | see how it performs under load and failures, and how to size it |
| [docs/LEARNINGS.md](docs/LEARNINGS.md) | read the real problems load testing found, why they happened and how they were fixed |
| [docs/PRD.md](docs/PRD.md), [HLD](docs/HLD.md), [LLD](docs/LLD.md) | read the design |

## Quick start (local)

You need Docker and the OpenSSH `sftp` client.

```sh
make up       # starts SFTPgate, an S3 mock, a GCS emulator, Prometheus and Grafana
make e2e      # runs end-to-end checks

sftp -P 2222 -i .dev/keys/demo_user demo@localhost
```

The demo account sees two folders, `/s3` and `/gcs`, each backed by a different
storage. Run `make down` to stop and delete everything.

| What | Where |
|---|---|
| SFTPgate web UI | <http://localhost:8080/ui/> (any username, password `dev-only-ui-token`) |
| Grafana dashboard | <http://localhost:3000> (opens without login; admin login is `admin` / `admin`) |
| Prometheus and alerts | <http://localhost:9090/alerts> |
| Traces (Jaeger) | <http://localhost:16686> |
| Raw metrics | <http://localhost:8080/metrics> |

The compose file is [`deploy/compose/docker-compose.yml`](deploy/compose/docker-compose.yml),
and the config it runs is [`deploy/compose/config/sftpgate.yaml`](deploy/compose/config/sftpgate.yaml).
Edit that file while it runs and SFTPgate reloads it within a few seconds.

## Post-upload actions

A folder can run steps after every upload. The uploaded file is never changed:
steps work on a copy and write new files.

```yaml
folders:
  - path: /inbound
    storage: recon-raw
    perms: [upload]
    on_upload:
      - pgp_decrypt: { key: "${file:/secrets/private.asc}", passphrase: "${env:PGP_PASS}" }
      - copy: { storage: warehouse, key: "razorpay/{date}/{filename}" }
      - webhook: { url: "https://recon.internal/hooks/sftpgate", secret: "${env:HOOK_SECRET}" }
```

| Step | What it does |
|---|---|
| `pgp_decrypt` | Decrypts the file (armored or binary). Strips `.pgp`, `.gpg` or `.asc` from the name. With `signer_keys` (the partner's public keys), the file must also be signed by one of them; unsigned files and unknown signers are rejected. |
| `pgp_encrypt` | Encrypts for `recipients` (armored public keys) and adds `.pgp`. |
| `copy` | Saves the file to a storage. The key can use `{account}`, `{folder}`, `{date}`, `{time}`, `{filename}` and `{job_id}`. |
| `webhook` | POSTs JSON describing the upload and the copies made so far. With a `secret`, the body is signed in the `X-Sftpgate-Signature: sha256=<hex HMAC>` header. |

Jobs wait in a queue on disk (`<spool_dir>/queue`), so they survive restarts.
A failed job is retried with a growing wait (30s, 1m, 2m and so on, up to 1h).
After `max_attempts` it moves to `queue/failed`. To run it again, move its file
back into `queue`. A job can run more than once, so webhook receivers should
ignore a `job_id` they have already seen.

## Scheduled transfers

SFTPgate can also be the client: pull new files from a partner's SFTP server
into a bucket, or push files from a bucket to the partner, on a schedule.

```yaml
remotes:
  bank:
    addr: sftp.bank.example:22
    user: acme
    host_key: "ssh-ed25519 AAAA..."               # pinned: required
    private_key: "${file:/secrets/bank_key}"      # and/or password: "${env:...}"

transfers:
  - name: bank-statements
    schedule: "0 6 * * *"                         # cron, in server.timezone
    pull:
      from: { remote: bank, path: /out, pattern: "*.csv" }
      to: { storage: recon-raw, key: "bank/{date}/{filename}" }
      delete_after: false
  - name: bank-payouts
    schedule: "*/30 * * * *"
    push:
      from: { storage: reports, prefix: payouts/outbox/, pattern: "*.csv" }
      to: { remote: bank, path: /in }
      after: "move:payouts/sent/"                 # keep | delete | move:<prefix>
```

- **Pull** skips files already in the bucket. With `delete_after`, the remote
  file is removed once it is stored. A skipped file is never deleted: SFTPgate
  only removes partner files it stored itself in that run.
- **Push** uploads under a temporary dot-name and renames it when complete, so
  the partner never sees half a file. With `after: keep` it skips files the
  remote already has.
- A run is skipped if the previous one is still going. One bad file doesn't
  stop the others, but the run is marked failed.
- With several replicas, set `SFTPGATE_TRANSFERS=off` on all but one.

## GitOps with git-sync

Keep the config in a git repository and let
[git-sync](https://github.com/kubernetes/git-sync) deliver it:

```sh
GITOPS_REPO=https://github.com/acme/sftpgate-config.git GITOPS_PATH=prod/sftpgate.yaml make up-gitops
```

A merged commit goes live within about 30 seconds. A commit with a broken
config is rejected and the old config keeps running. The web UI, the API and
the `sftpgate_config_info` metric show the git commit that is live. See
[`deploy/compose/gitops.yml`](deploy/compose/gitops.yml). On Kubernetes, run
git-sync as a sidecar sharing an `emptyDir` with SFTPgate in the same way.

## Web UI and API

A read-only web page and JSON API show open sessions, recent events, the
post-upload queue and the running config. They are off until you set tokens:

```yaml
http:
  tokens: ["${env:SFTPGATE_UI_TOKEN}"]   # at least 16 characters
```

In a browser, log in with any username and a token as the password. Scripts
send `Authorization: Bearer <token>`.

| Endpoint | Returns |
|---|---|
| `GET /api/v1/status` | version, uptime, config hash, readiness, session and queue counts |
| `GET /api/v1/sessions` | open sessions with bytes moved |
| `GET /api/v1/events?event=&account=&result=&limit=` | the newest audit events kept in memory (up to 2000) |
| `GET /api/v1/actions` | post-upload jobs waiting and failed |
| `GET /api/v1/config` | the config file as written (secrets only as references) |

Nothing can be changed here. There are only GET routes; changes go through git.
The events list is in memory and starts empty after a restart. Full history
is in your log system.

## Monitoring

Ready-to-use files you can copy into your own setup:

- [`deploy/helm/sftpgate/files/alerts.yml`](deploy/helm/sftpgate/files/alerts.yml):
  alerts for the server being down, login failure spikes, blocked IPs, storage
  errors, rejected config reloads, a low spool disk and failing post-upload actions.
- [`deploy/helm/sftpgate/files/dashboard.json`](deploy/helm/sftpgate/files/dashboard.json):
  a dashboard with sessions, uploads, downloads, logins, traffic per account,
  transfer times, storage errors, denied requests and the running config version.

### Tracing

Set `OTEL_EXPORTER_OTLP_ENDPOINT` (for example `http://otel-collector:4318`)
to send OpenTelemetry traces over OTLP/HTTP. All the other standard `OTEL_*`
variables work too. Without it, tracing is off and costs nothing.

Each session is one trace with a span per upload, download and file command.
Post-upload jobs get their own trace, linked to the upload, and webhooks
carry a `traceparent` header so the receiver can continue the trace. Audit
lines include `trace_id`. Spans never contain file names or object keys,
because tracing tools are often less locked down than audit logs.

Mount the config **folder**, not the single file, in Docker and Kubernetes.
Editors and git replace files instead of changing them, and a single-file mount
keeps showing the old file, so hot reload would quietly stop working.

## Tested clients

| Client | Used by | Key | Password | Key **and** password |
|---|---|---|---|---|
| OpenSSH `sftp` | Linux and macOS scripts | ✅ | ✅ | ✅ |
| PuTTY `psftp` 0.78 | the engine inside **WinSCP** and **FileZilla** | ✅ ed25519, RSA | ✅ | ✅ |
| Paramiko 3.5 | Python scripts, Airflow | ✅ ed25519, RSA | ✅ | ❌ (Paramiko limitation; use a key) |
| Go `x/crypto/ssh` + `pkg/sftp` | Go services | ✅ | ✅ | ✅ |
| Software that only speaks SHA-1 (for example JSch 0.1.55) | very old Java systems | refused on purpose; the partner must upgrade | | |

Each test uploads, lists, downloads and compares checksums.

## Deploying

### Docker

```sh
ssh-keygen -t ed25519 -N "" -f ssh_host_ed25519_key
docker run -d --name sftpgate --read-only --tmpfs /tmp \
  -p 2222:2222 -p 8080:8080 \
  -v "$PWD/config:/etc/sftpgate/config:ro" \
  -v "$PWD/ssh_host_ed25519_key:/etc/sftpgate/keys/ssh_host_ed25519_key:ro" \
  -v sftpgate-data:/var/lib/sftpgate \
  -e SFTPGATE_CONFIG=/etc/sftpgate/config/sftpgate.yaml \
  ghcr.io/shashankpatil-dev/sftpgate:0.3.0
```

The image is distroless and runs as user 65532 with a read-only root
filesystem. `docker ps` shows its health from the built-in `sftpgate healthcheck`.
Mount the config *folder*, not the file, so hot reload sees changes.

### Kubernetes with Helm

```sh
kubectl create namespace sftpgate
ssh-keygen -t ed25519 -N "" -f ssh_host_ed25519_key
kubectl -n sftpgate create secret generic sftpgate-host-keys --from-file=ssh_host_ed25519_key
helm install sftpgate deploy/helm/sftpgate -n sftpgate -f my-values.yaml
```

The chart runs a StatefulSet, so each pod gets its own disk for the spool and
the post-upload queue, behind a LoadBalancer that keeps partners' real IPs. It
supports secrets as env vars or files, git-sync, ServiceMonitor and
PrometheusRule, a Grafana dashboard ConfigMap, an Ingress for the UI, and
several replicas (scheduled transfers then run on the first pod only). See
[`values.yaml`](deploy/helm/sftpgate/values.yaml) for every setting.

### Kubernetes without Helm

[`deploy/kubernetes/`](deploy/kubernetes) has the same thing as plain manifests.

## Configuration

See [`examples/sftpgate.yaml`](examples/sftpgate.yaml) for a full example. A
minimal account looks like this:

```yaml
accounts:
  - name: razorpay
    auth:
      mode: all                      # key AND password; "any" means either one
      public_keys: ["ssh-ed25519 AAAA... razorpay-prod"]
      password_hash: "${env:RAZORPAY_PW_HASH}"
    allow_ips: ["52.66.0.0/16"]
    folders:
      - path: /inbound
        storage: recon-raw
        prefix: razorpay/
        perms: [list, upload]
        patterns: ["*.csv"]
        max_file_size: 500MiB
```

Folder permissions: `list`, `download`, `upload`, `overwrite`, `delete`,
`rename`, `mkdir`.

Without `overwrite`, a file can never be replaced, even when two partners
upload the same name at the same moment: the bucket itself refuses the second
save, and that partner gets "file already exists".

### Limits

| Setting | Default | What it protects |
|---|---|---|
| `server.max_connections` | 25 | the server, from a flood of connections from all accounts together. SFTP work is batch work: a few partners at a time. Raise it for busy servers. |
| `accounts[].max_sessions` | no limit | one account from using up the server |
| `folders[].max_file_size` | no limit | the disk, from one huge file; an upload only starts if this much space is free |
| `server.min_spool_free` | 1GiB | free disk left over for everything else |
| `server.throttle` | 5 failures / 10m, blocked 15m | logins, from password guessing |

Uploads wait on local disk until complete, then go to the bucket in one step.
Size that disk as largest `max_file_size` × uploads at the same time.

### Secrets

The YAML never holds secret values. Secret fields take a reference that is
filled in at runtime:

- `${env:NAME}`: an environment variable
- `${file:/path}`: a file, for example a mounted Kubernetes or Docker secret
- `${secret:URL}`: a cloud secret store, read with the same credentials as the buckets:
  - `gcpsecretmanager://projects/PROJECT/secrets/NAME`
  - `awssecretsmanager://NAME?region=ap-south-1`
  - `awsparamstore://NAME?region=ap-south-1`

Send `SIGHUP` after rotating a secret: every reference is read again on reload.

`sftpgate validate` refuses a config with a password hash written inline.
Public keys are not secret and can be written inline.

Make a password hash with:

```sh
sftpgate hash-password
```

### Check a config in CI

```sh
sftpgate validate -offline sftpgate.yaml
```

`-offline` skips the checks that need secrets or network access, so it runs
safely on pull requests.

### Reloading

SFTPgate checks the config file every 10 seconds and on `SIGHUP`. A valid new
config goes live right away. Only sessions of accounts that changed are
disconnected. An invalid file is rejected, and the old config keeps running.

## Endpoints

| Port | Path | What |
|---|---|---|
| 2222 | | SFTP |
| 8080 | `/metrics` | Prometheus metrics |
| 8080 | `/healthz` | process is up |
| 8080 | `/readyz` | config loaded and every storage reachable |
| 8080 | `/ui/`, `/api/v1/` | read-only web UI and API (when `http.tokens` is set) |

## Development

```sh
make test     # unit and integration tests
make lint     # golangci-lint
make validate # check the example configs
make check-monitoring # check the Prometheus config and test the alert rules
make check-k8s        # lint the Helm chart and validate it against Kubernetes schemas
make k8s-smoke        # install the chart on a local kind cluster and test it
```

Tests live under `test/`. Design docs are in [`docs/`](docs): [PRD](docs/PRD.md),
[HLD](docs/HLD.md), [LLD](docs/LLD.md).

## License

Apache-2.0
