---
title: "Example: send files to an auditor"
description: How a team adds scheduled SFTP delivery to a service it already has.
---

A worked example of how a team adds SFTPgate to a service it already has, from the first file to production. To receive files in the same service, see [the next example](../example-receive-from-gateways/).

## The situation

- **revenue-assurance** is an existing Kotlin (Ktor) service with its own
  git repository, CI and Kubernetes deployment.
- Every day it **collects GST files** into a Google Cloud Storage bucket:
  `gs://acme-revenue-assurance-gst`.
- **The auditor** (the tax advisor) wants these files on **its SFTP server**, every day
  at **10:00 and 18:00** India time.

Without SFTPgate, the team would write SFTP code in Kotlin: connecting,
retries, keys, scheduling, avoiding duplicates. With SFTPgate, **they write one
YAML file**, and no Kotlin changes are needed beyond writing files to the
right folder.

```
revenue-assurance (Kotlin)          SFTPgate (in the same cluster)           the auditor
──────────────────────────          ──────────────────────────────         ──────
writes GST files to          ──►    at 10:00 and 18:00:              ──►   sftp.auditor.example
gs://…-gst/gst/outbox/              picks up the files in outbox/          /incoming/
                                    sends each one over SFTP
                                    moves it to gst/sent/
```

## 1. What the team adds to its repository

Only a new `sftp/` folder and one CI file. The Kotlin code stays as it is.

```
revenue-assurance/
├── src/                          ← existing Kotlin code (unchanged)
├── build.gradle.kts              ← existing
├── k8s/ or helm/                 ← existing deployment of the Kotlin service
├── sftp/                         ← NEW
│   ├── sftpgate.yaml             ← WHAT to transfer: the SFTPgate config
│   └── values.yaml               ← HOW to run it in Kubernetes (Helm settings)
└── .github/workflows/
    └── sftp.yml                  ← NEW: check the config, then deploy it
```

**Why in the same repo?** The people who own the GST files also own where they
go. A change like "the auditor wants the files at 09:00" is a one-line pull request,
reviewed like any code change, with full history in git.

## 2. `sftp/sftpgate.yaml`: what to transfer

```yaml
# SFTPgate config for revenue-assurance.
# Sends the GST files that revenue-assurance prepares to the auditor, twice a day.
# No secret values here: only references, filled in from Kubernetes Secrets.
version: 1

server:
  host_keys: [/etc/sftpgate/keys/ssh_host_ed25519_key]
  timezone: Asia/Kolkata          # schedules below are India time

# The bucket revenue-assurance already writes GST files into.
storages:
  ra-gst: "gs://acme-revenue-assurance-gst"

# the auditor's SFTP server.
remotes:
  auditor:
    addr: sftp.auditor.example:22
    user: acme-gst
    host_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
    private_key: "${file:/etc/sftpgate/secrets/auditor/id_ed25519}"

transfers:
  - name: gst-to-auditor
    schedule: "0 10,18 * * *"     # every day at 10:00 and 18:00
    push:
      from: { storage: ra-gst, prefix: gst/outbox/, pattern: "*" }
      to: { remote: auditor, path: /incoming }
      after: "move:gst/sent/"     # never send the same file twice
```

What each part does:

| Part | Meaning |
|---|---|
| `storages.ra-gst` | A short name for the existing bucket. No password: the pod's Google identity gives access (step 4). |
| `remotes.auditor` | The auditor's server: address, the user name the auditor gave us, **the auditor's host key** (so we never send tax data to a fake server), and **our private key** for logging in. |
| `schedule: "0 10,18 * * *"` | Cron format, "minute hour day month weekday": minute 0 of hours 10 and 18, every day. |
| `prefix: gst/outbox/` | Only files directly inside `gst/outbox/` are sent (not sub-folders). |
| `to.path: /incoming` | The folder on the auditor's server. Each file is written under a temporary name and renamed when complete, so **the auditor never sees a half file**. |
| `after: "move:gst/sent/"` | After a successful send, the file moves to `gst/sent/`. The outbox then only holds files still to send. |

There's no `accounts:` section, because nobody logs in to us; we only send out.

For every other setting, see [the complete example](../../reference/complete-example/).

## 3. The one change in the Kotlin service

revenue-assurance already writes to the bucket. It only needs to put
**ready-to-send** files in `gst/outbox/`, with a unique name:

```
gs://acme-revenue-assurance-gst/gst/outbox/GSTR1_2026-10_run-0930.json
```

- **No half files:** a GCS file only becomes visible once it's completely
  written, so SFTPgate can never pick up a file the Kotlin code is still
  writing.
- **Use unique names** (date, run number) so a new file never replaces one not
  yet sent.
- If the service builds files in steps, build them elsewhere (for example
  `gst/work/`) and write the final file to `gst/outbox/` last.

## 4. One-time setup (done once by the platform or DevOps team)

| # | What | How |
|---|---|---|
| 1 | **A namespace** | `kubectl create namespace revenue-assurance-sftp` (or reuse the service's own) |
| 2 | **Bucket access without keys** (GKE workload identity) | Create a Google service account, for example `ra-sftpgate@acme.iam.gserviceaccount.com`. Give it **Storage Object User** on **this bucket only** (it reads, and moves = copy + delete). Link it to the Kubernetes service account, then put the annotation in `values.yaml` (below). |
| 3 | **SFTPgate's own host key** | `ssh-keygen -t ed25519 -N "" -f ssh_host_ed25519_key`, then `kubectl -n revenue-assurance-sftp create secret generic sftpgate-host-keys --from-file=ssh_host_ed25519_key` |
| 4 | **Our login key for the auditor** | `ssh-keygen -t ed25519 -N "" -f id_ed25519`. Send **`id_ed25519.pub`** (the public half) to the auditor. Store the private half: `kubectl -n revenue-assurance-sftp create secret generic auditor-sftp-key --from-file=id_ed25519` |
| 5 | **the auditor's host key** | Ask the auditor for it, or run `ssh-keyscan -t ed25519 sftp.auditor.example`, **confirm it with the auditor by phone or email**, then paste it into `host_key`. |
| 6 | **Our outgoing IP** | The auditor will almost certainly allow only known IPs. Pods leave the cluster through **Cloud NAT**; give it a **static IP** and send that IP to the auditor. |

Store keys in your secret manager (for example GCP Secret Manager with External
Secrets) rather than on laptops. Delete the local copies after creating the
Secrets.

## 5. `sftp/values.yaml`: how it runs in Kubernetes

```yaml
# Helm settings for revenue-assurance's SFTPgate. No secrets here.
replicas: 1                 # transfers run on one pod anyway; 1 is enough

image:
  tag: "0.4.0"              # pin the version; upgrade with a reviewed PR

serviceAccount:
  annotations:              # step 2: the pod acts as this Google account
    iam.gke.io/gcp-service-account: ra-sftpgate@acme.iam.gserviceaccount.com

hostKeys:
  existingSecret: sftpgate-host-keys          # step 3

secretFiles:                                  # step 4, appears at this path
  - secretName: auditor-sftp-key
    mountPath: /etc/sftpgate/secrets/auditor

service:
  sftp:
    type: ClusterIP         # nobody connects IN, so no public address

persistence:
  size: 2Gi                 # working space while a file is being sent

metrics:
  serviceMonitor:
    enabled: true           # Prometheus collects the metrics
  prometheusRule:
    enabled: true           # the 11 alerts, including SftpgateTransferFailing
```

The config itself isn't in this file. CI passes `sftpgate.yaml` to Helm
directly (next step), so there is **one** source of truth.

## 6. CI: check every change, deploy on merge

`.github/workflows/sftp.yml`:

```yaml
name: sftp
on:
  pull_request:
    paths: ["sftp/**"]
  push:
    branches: [main]
    paths: ["sftp/**"]

jobs:
  # Every pull request: is the config valid? A typo never reaches production.
  validate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Validate sftpgate.yaml
        run: |
          docker run --rm -v "$PWD/sftp:/cfg:ro" \
            ghcr.io/shashankpatil-dev/sftpgate:0.4.0 \
            validate -offline /cfg/sftpgate.yaml

  # After merge to main: send the new config to the cluster.
  deploy:
    if: github.event_name == 'push'
    needs: validate
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Get the SFTPgate chart (same version as the image)
        run: git clone --depth 1 --branch v0.4.0 https://github.com/shashankpatil-dev/sftpgate /tmp/sftpgate
      # Log in to your cluster here, the same way the Kotlin service's deploy does,
      # for example google-github-actions/auth + get-gke-credentials.
      - name: Deploy
        run: |
          helm upgrade --install ra-sftpgate /tmp/sftpgate/deploy/helm/sftpgate \
            --namespace revenue-assurance-sftp \
            -f sftp/values.yaml \
            --set-file config=sftp/sftpgate.yaml \
            --wait
```

- **`paths: ["sftp/**"]`**: this workflow runs only when the SFTP files change,
  never for Kotlin changes. The Kotlin pipeline stays exactly as it is.
- **`--set-file config=...`**: Helm reads `sftpgate.yaml` and puts it into a
  Kubernetes **ConfigMap** that the pod reads.
- **`validate -offline`** checks the structure, schedules, key formats and
  names without needing real secrets, so it runs safely on any pull request.

## 7. How Kubernetes picks up a change

Say the auditor asks for 09:00 instead of 10:00:

```
1. A developer changes  "0 10,18 * * *"  →  "0 9,18 * * *"  and opens a PR
2. CI validates it                                     (≈ 30 s)
3. A teammate reviews and merges
4. CI runs helm upgrade → the ConfigMap is updated     (≈ 30 s)
5. Kubernetes updates the file inside the pod          (up to ≈ 1 min)
6. SFTPgate notices within reload_interval (10 s), checks the new file,
   and switches to it. NO RESTART. The new schedule is live.
```

If the new file is broken (for example a wrong key format), SFTPgate
**keeps running the old config**, logs why, and the
`SftpgateConfigReloadFailed` alert fires. A bad change can't take the
service down.

What needs a restart and what doesn't:

| Change | Restart? |
|---|---|
| Anything in `sftpgate.yaml` (schedule, paths, new remote, new transfer) | **No**, hot reload |
| A secret mounted as a file (`${file:...}`), for example rotating the the auditor key | **No**, picked up on the next check |
| A secret used as an environment variable (`${env:...}`) | Yes: `kubectl rollout restart statefulset/ra-sftpgate` |
| `values.yaml` (image version, resources, service account) | Yes, Helm rolls the pod automatically |

A restart between runs loses nothing: files simply wait in `gst/outbox/`.

## 8. Day to day

- **revenue-assurance** writes files to `gst/outbox/` whenever it likes.
- At **10:00 and 18:00**, SFTPgate sends everything waiting there, then moves
  each sent file to `gst/sent/`.
- **If a run fails** (the auditor's server down, network problem): the files **stay in
  the outbox** and go at the next run. Nothing is lost or sent twice. The
  `SftpgateTransferFailing` alert tells the team.
- **If the pod is down at 10:00:** that run is skipped (schedules don't catch
  up), and the files go at 18:00. If that's too late, add a retry slot, for
  example `"0 10,11,18 * * *"`. Extra runs are harmless when the outbox is
  empty.

### Watching it

| Where | What you see |
|---|---|
| **Logs** (`kubectl logs`, Cloud Logging) | One JSON audit line per file sent, with the transfer name, file name and result, plus a summary per run |
| **Metric** `sftpgate_transfer_runs_total{transfer="gst-to-auditor"}` | Runs by result (ok or failed) |
| **Grafana** | The SFTPgate dashboard (`metrics.grafanaDashboard.enabled: true`) |
| **Alerts** | `SftpgateTransferFailing`, `SftpgateStorageErrors` (bucket problems), `SftpgateDown` |
| **The bucket** | `gst/outbox/` = waiting, `gst/sent/` = delivered |

## 9. Going live safely

1. **Test against the auditor's test server first**, if they have one: same config,
   with `addr` pointing at it.
2. **Run it now instead of waiting for 10:00:** there's no "run now" button
   yet, so open a PR that temporarily sets `schedule: "*/5 * * * *"`, put one
   test file in `gst/outbox/`, check that it arrives at the auditor and moves to
   `gst/sent/`, then set the real schedule back.
3. **Agree the file format and names with the auditor**, and who to call when a file is
   missing.
4. **Keep `gst/sent/` for audit**, and add a bucket lifecycle rule to delete
   it after your retention period, for example 8 years for GST records.

## Other cases with the same setup

| If… | Add to `sftpgate.yaml` |
|---|---|
| The auditor prefers to **fetch** the files themselves | an `accounts:` entry for the auditor with a folder on `ra-gst` prefix `gst/outbox/` and `perms: [list, download]`, and `service.sftp.type: LoadBalancer` in `values.yaml` |
| The auditor **sends files back** (acknowledgements) to their server | a second transfer with `pull:` from `auditor` `/outgoing` into `ra-gst` key `gst/acks/{date}/{filename}` |
| revenue-assurance must be **told** when an ack arrives, or when a file reached the auditor | a `webhook` action, run from the pull's `hooks: { after_pull: [notify-ra] }` or the push's `hooks: { after_push: [notify-ra] }` |
| Files must be **encrypted** for the auditor with PGP | a `pgp_encrypt` action with the auditor's public key, in `hooks: { before_push: [encrypt-auditor] }`. The auditor receives `<name>.pgp`; your outbox keeps the plain file. |
| A broken GST file must **never** be sent | a `check` action (size, CSV header, rows) first in `before_push`. A file that fails stays in the outbox, and the run fails so `SftpgateTransferFailing` fires. |

For example, check, encrypt and notify:

```yaml
actions:
  gst-csv:    { check: { min_size: 100, csv: { header: [gstin, invoice_no, invoice_date, taxable_value], min_rows: 1 } } }
  encrypt-auditor: { pgp_encrypt: { recipients: ["${file:/etc/sftpgate/secrets/auditor/auditor-pgp.pub.asc}"] } }
  notify-ra:  { webhook: { url: "http://revenue-assurance.revenue-assurance.svc.cluster.local:8080/internal/sftp/events", secret: "${env:RA_WEBHOOK_SECRET}" } }

transfers:
  - name: gst-to-auditor
    schedule: "0 10,18 * * *"
    push:
      from: { storage: ra-gst, prefix: gst/outbox/, pattern: "*" }
      to: { remote: auditor, path: /incoming }
      after: "move:gst/sent/"
      rejected: "move:gst/rejected/"   # a bad file moves here once, instead of failing every run
    hooks:
      before_push: [gst-csv, encrypt-auditor]
      after_push: [notify-ra]
```
