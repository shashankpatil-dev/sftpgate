---
title: Upgrade
description: How to move to a new version safely.
---

Before 1.0, a minor version may include breaking changes. They're always listed first in the [changelog](../../project/changelog/), with how to upgrade.

## Steps

1. Read the changelog for every version you're crossing.
2. Check your config with the new version:

   ```sh
   docker run --rm -v "$PWD:/c:ro" \
     ghcr.io/shashankpatil-dev/sftpgate:<new-version> validate -offline /c/sftpgate.yaml
   ```

3. Roll it out with [Docker](../../deploy/docker/#update-to-a-new-version) or [Helm](../../deploy/kubernetes/#make-changes).

An old setting is refused with a message naming the line and where it moved, so nothing breaks silently.

## From 0.3 to 0.4

### Move the queue settings

The top-level `actions:` now holds named actions. Move `workers`, `max_attempts` and `backoff` to `server.queue`:

```yaml
# before
actions:
  workers: 4
  max_attempts: 8
  backoff: 30s

# after
server:
  queue: { workers: 4, max_attempts: 8, backoff: 30s }
```

### Stop using the web UI

`/ui/` answers 404, and `/` no longer redirects. The read-only JSON API is unchanged. Use it, or Grafana, logs and traces. A 401 now asks for a `Bearer` token, and a Basic login with the token as password still works.

### Helm

The Ingress routes only `/api`. The chart version is now 0.2.0.

### What keeps working

Jobs already queued by v0.3.0 still run after the upgrade. `on_upload:` still works as an alias for `hooks.after_upload`.
