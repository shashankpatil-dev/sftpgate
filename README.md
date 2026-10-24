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
| Grafana dashboard | <http://localhost:3000> (opens without login; admin login is `admin` / `admin`) |
| Prometheus and alerts | <http://localhost:9090/alerts> |
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
| `pgp_decrypt` | Decrypts the file (armored or binary). Strips `.pgp`, `.gpg` or `.asc` from the name. |
| `pgp_encrypt` | Encrypts for `recipients` (armored public keys) and adds `.pgp`. |
| `copy` | Saves the file to a storage. The key can use `{account}`, `{folder}`, `{date}`, `{time}`, `{filename}` and `{job_id}`. |
| `webhook` | POSTs JSON describing the upload and the copies made so far. With a `secret`, the body is signed in the `X-Sftpgate-Signature: sha256=<hex HMAC>` header. |

Jobs wait in a queue on disk (`<spool_dir>/queue`), so they survive restarts.
A failed job is retried with a growing wait (30s, 1m, 2m and so on, up to 1h).
After `max_attempts` it moves to `queue/failed`. To run it again, move its file
back into `queue`. A job can run more than once, so webhook receivers should
ignore a `job_id` they have already seen.

## Monitoring

Ready-to-use files you can copy into your own setup:

- [`deploy/compose/prometheus/alerts.yml`](deploy/compose/prometheus/alerts.yml):
  alerts for the server being down, login failure spikes, blocked IPs, storage
  errors, rejected config reloads, a low spool disk and failing post-upload actions.
- [`deploy/compose/grafana/dashboards/sftpgate.json`](deploy/compose/grafana/dashboards/sftpgate.json):
  a dashboard with sessions, uploads, downloads, logins, traffic per account,
  transfer times, storage errors, denied requests and the running config version.

Mount the config **folder**, not the single file, in Docker and Kubernetes.
Editors and git replace files instead of changing them, and a single-file mount
keeps showing the old file, so hot reload would quietly stop working.

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

## Development

```sh
make test     # unit and integration tests
make lint     # golangci-lint
make validate # check the example configs
make check-monitoring # check the Prometheus config and test the alert rules
```

Tests live under `test/`. Design docs are in [`docs/`](docs): [PRD](docs/PRD.md),
[HLD](docs/HLD.md), [LLD](docs/LLD.md).

## License

Apache-2.0
