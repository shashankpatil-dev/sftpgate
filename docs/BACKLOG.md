# Backlog

Ideas and known gaps, not scheduled yet. Newest first.

## Features

- **Scoped API tokens**, limited to some accounts or transfers.
- **Webhooks for system events.** Actions and hooks (v0.4) made webhooks
  work for uploads, downloads, pushes and pulls. Still missing: events that
  aren't about one file, like `transfer_failed`, `action_gave_up` and
  `auth_failed_spike`, so teams can notify Slack without Alertmanager.
- **More checks.** Duplicate content (a SHA-256 already received under another
  name, which needs a store of seen hashes), Excel sheets, and JSON schema.
- **`rejected/` for push transfers.** Today a file that fails `before_push`
  stays in the outbox and fails every run until someone fixes it. Moving it to
  a configurable prefix would stop the repeat failures.
- **Expected-file alerts.** "Razorpay's file must arrive by 11:00": a metric or
  alert when an expected upload or transfer file is missing.
- **`enabled: false` on an action, hook or transfer**, to pause it without deleting it.
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
