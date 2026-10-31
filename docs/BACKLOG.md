# Backlog

Ideas and known gaps, not scheduled yet. Newest first.

## Features

- **Scoped API tokens**, limited to some accounts or transfers.
- **More checks.** Duplicate content (a SHA-256 already received under another
  name, which needs a store of seen hashes), Excel sheets, and JSON schema.
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

- CONTRIBUTING.md.

## Not planned, and why

- **Webhooks for system events** (`transfer_failed`, `action_gave_up`,
  `auth_failed_spike`, `config_rejected`). Decided against on 2026-10-29.
  Each one is already a Prometheus alert in `alerts.yml`
  (`SftpgateTransferFailing`, `SftpgateActionsGaveUp`,
  `SftpgateLoginFailureSpike`, `SftpgateConfigReloadFailed`), and
  Alertmanager already sends alerts to Slack, email or PagerDuty, with
  grouping, silencing and repeats. Rebuilding that inside SFTPgate would be a
  weaker copy, and spike detection would count per pod. Webhooks stay for
  file events (upload, download, push, pull), which only SFTPgate knows
  about. See "Sending alerts to Slack" in the README.
- **Expected-file alerts** ("Razorpay's file must arrive by 11:00"). Decided
  against on 2026-10-31. The rule belongs to the business, not the gateway, and
  each team's calendar differs. Teams can run this outside SFTPgate with their
  bucket's monitoring or a scheduled job that checks the bucket. A pull that
  fails already raises `SftpgateTransferFailing`.
