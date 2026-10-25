# Backlog

Ideas and known gaps, not scheduled yet. Newest first.

## Features

- **Remove the HTML UI; keep only the essential JSON API.** Grafana, logs and
  traces already show sessions and events better, and the UI adds code and a
  login surface. Keep, off by default and token-protected: open sessions,
  pending and failed actions, plus two new calls: **run a transfer now** and
  **retry a failed action**. Scoped tokens (limited to some accounts) apply to
  the API only.
- **Complete, well-named traces usable for alerts and visibility.** Today only
  `sftp.session`, `storage.write`, `action.run` and `action.<step>` exist. Add:
  - `sftp.login` (method, result);
  - one span per file: `sftp.upload` and `sftp.download` (account, folder,
    file, size, sha256, result);
  - `storage.read`;
  - `transfer.run` and `transfer.file` (scheduled jobs have no trace today).

  Use consistent attribute names and error status on failures, so trace-based
  metrics (span metrics in Tempo, Jaeger or Datadog) can drive alerts, for
  example "p95 of `sftp.upload` for razorpay".
- **Better webhook publishing.** Today a webhook is only an `on_upload` step.
  Add event webhooks for `upload`, `transfer_done`, `transfer_failed`,
  `action_gave_up` and `auth_failed_spike`, with the same HMAC signing and
  retries, so teams can notify Slack or their own service without
  Alertmanager.
- **`before_upload` checks on incoming files.** Uploads already wait on the
  spool disk until complete, so they can be checked **before** they're saved
  and before the partner is told "OK". A failed check refuses the upload with
  a clear message to the partner, and the file is never stored. Checks: min and
  max size, non-empty, CSV header or column count, row count > 0, and
  duplicate content (a SHA-256 already received under another name).
- **Expected-file alerts.** "Razorpay's file must arrive by 11:00": a metric or
  alert when an expected upload or transfer file is missing.
- **`before_send` steps for push transfers.** Checks and changes before a file
  is sent to a partner: `check` (min/max size, non-empty), `pgp_encrypt` for the
  vendor's key, verify the stored SHA-256. A file that fails goes to a
  `rejected/` prefix and is reported instead of sent.
- **`enabled: false` on a step or transfer**, to pause it without deleting it.
- **Streaming uploads** for sequential writers, skipping the spool disk.
- **Publish the Helm chart** as an OCI artifact on release, so teams don't need
  to clone the repository to install it.

## Hardening and proof

- Fuzz test for path resolution (keys must always stay under the folder prefix).
- Image scanning (Trivy), SBOM and signed images in the release workflow.
- Soak test (hours of steady load) to look for slow leaks.
- Client compatibility: JSch (Java), Cyberduck. PuTTY (WinSCP, FileZilla),
  Paramiko and OpenSSH are done.
- Tests against real GCS, S3 and Azure, including workload identity.
- Throttle shared across replicas (today each pod counts failed logins on its own).
- A plan for jobs left on the disk of a pod that is gone for good.

## Project

- CONTRIBUTING.md, CHANGELOG.md.
