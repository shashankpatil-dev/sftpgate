# Changelog

All notable changes. Versions follow [semantic versioning](https://semver.org);
before 1.0, a minor version (0.x) may include breaking changes, always listed
first with how to upgrade.

## v0.4.0 (2026-10-29)

### Breaking changes and how to upgrade

1. **Queue settings moved to `server.queue`.** The top-level `actions:` now
   holds named actions. Move `workers`, `max_attempts` and `backoff`:

   ```yaml
   # before                    # after
   actions:                    server:
     workers: 4                  queue: { workers: 4, max_attempts: 8, backoff: 30s }
     max_attempts: 8
     backoff: 30s
   ```

   An old file is refused with a message naming the line, so nothing breaks
   silently. Run `sftpgate validate` before deploying.
2. **The HTML web UI is removed.** `/ui/` answers 404 and `/` no longer
   redirects. The read-only JSON API (`/api/v1/...`) is unchanged; use it, or
   Grafana, logs and traces. A 401 now asks for a `Bearer` token (a Basic login
   with the token as password still works).
3. **Helm:** the Ingress routes only `/api`. The chart version is now 0.2.0.
4. **Example env var renamed** from `SFTPGATE_UI_TOKEN` to `SFTPGATE_API_TOKEN`
   in the examples and the local setup. Your own config keeps whatever
   reference name it uses.

Jobs already queued by v0.3.0 still run after the upgrade. `on_upload:` still
works as an alias for `hooks.after_upload`.

### Added

- **Named actions and hooks.** Define `check`, `pgp_decrypt`, `pgp_encrypt`,
  `copy` and `webhook` actions once under `actions:`, and run them by name from
  hooks: `before_upload`, `after_upload`, `after_download` on folders;
  `before_push`, `after_push`, `before_pull`, `after_pull` on transfers.
- **`check` action:** min/max size and CSV rules (exact header, minimum rows,
  same column count, delimiter). In `before_upload` a bad file is refused
  with the reason and never stored.
- **Webhooks for downloads, pushes and pulls**, custom `headers:` (secrets
  only as references), and `transfer`, `remote`, `remote_path`, `time` in the
  payload.
- **`push.rejected: "move:<prefix>"`:** a file refused by `before_push` moves
  out of the outbox, so it fails one run instead of every run.
- **`enabled: false`** pauses a transfer, or switches an action off in every
  hook (including queued jobs). `pgp_encrypt` can't be switched off.
- **Admin API, off by default:** `POST /api/v1/transfers/{name}/run` and
  `POST /api/v1/actions/retry[?job=<id>]`. Each must be listed in
  `http.admin.allow`, takes only `http.admin.tokens` as a Bearer token
  (separate from read-only tokens), and is audited.
- **Complete tracing:** `sftp.login`, `storage.read`, `transfer.run`,
  `transfer.connect`, `transfer.file`; one set of `sftpgate.*` attribute
  names; `sftpgate.result` = `ok` / `denied` / `failed` on every span, with
  only `failed` marked as an error, so span metrics can drive alerts.
- `examples/sftpgate.full.yaml` (every setting, commented) and two
  walkthroughs for an existing service (outbound to EY, inbound from payment
  gateways). README section on sending alerts to Slack with Alertmanager.
- `CHANGELOG.md`.

### Changed

- A failed login is now traced (as `denied`); health probes are not.
- `sftpgate_transfer_files_total` can have `result="denied"` for files a
  before hook refused.
- Transfer audit events carry `trace_id`.

### Fixed

- Span errors could contain object keys (file names) from storage errors;
  they now keep only the error code.
- A PGP recipient key that can't encrypt (sign-only) is refused by
  `validate` instead of failing at run time.
- Secrets reused through YAML aliases were not resolved.
- `make manifests` no longer empties the generated manifest when `helm` is
  missing.

## v0.3.0 (2026-10-24)

Read-only web UI and JSON API, scheduled pull and push transfers with pinned
host keys, git-sync for GitOps config, the load test fixes, the Helm chart,
`LICENSE` (Apache-2.0) and `SECURITY.md`.
