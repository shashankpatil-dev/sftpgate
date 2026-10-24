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
make up       # starts SFTPgate with an S3 mock and a GCS emulator
make e2e      # runs end-to-end checks

sftp -P 2222 -i .dev/keys/demo_user demo@localhost
```

The demo account sees two folders, `/s3` and `/gcs`, each backed by a different
storage. Metrics are at <http://localhost:8080/metrics>. Run `make down` to stop.

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
```

Tests live under `test/`. Design docs are in [`docs/`](docs): [PRD](docs/PRD.md),
[HLD](docs/HLD.md), [LLD](docs/LLD.md).

## License

Apache-2.0
