# Backlog

Ideas and known gaps, not scheduled yet. Newest first.

## Features

- **`before_send` steps for push transfers.** Checks and changes before a file
  is sent to a partner: `check` (min/max size, non-empty), `pgp_encrypt` for the
  vendor's key, verify the stored SHA-256. A file that fails goes to a
  `rejected/` prefix and is reported instead of sent.
- **Scoped UI/API tokens.** A token limited to some accounts, so each internal
  team sees only its own partners.
- **`enabled: false` on a step or transfer**, to pause it without deleting it.
- **Streaming uploads** for sequential writers, skipping the spool disk.

## Hardening and proof

- Fuzz test for path resolution (keys must always stay under the folder prefix).
- Image scanning (Trivy), SBOM and signed images in the release workflow.
- Load test against the PRD target (200 sessions, large files) and publish numbers.
- Client compatibility checks: WinSCP, FileZilla, Paramiko, JSch, Cyberduck.
- Tests against real GCS, S3 and Azure, including workload identity.
- Throttle shared across replicas (today each pod counts failed logins on its own).
- A plan for jobs left on the disk of a pod that is gone for good.

## Project

- LICENSE, CONTRIBUTING.md, SECURITY.md, CHANGELOG.md.
- First tagged release (v0.3.0) and published image.
