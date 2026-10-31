---
title: server
description: Every setting of the SFTP server, with its default.
---

How the SFTP side behaves, where partners connect.

```yaml
server:
  host_keys: [/etc/sftpgate/keys/ssh_host_ed25519_key]
  timezone: Asia/Kolkata
  max_connections: 50
  throttle: { max_failures: 5, window: 10m, block: 15m }
  queue: { workers: 4, max_attempts: 8, backoff: 30s }
```

## Network

### `listen`

Address. Default `":2222"`.

The address and port for SFTP inside the container. You rarely need to change it; map the port outside instead.

### `allow_ips`

List of IP ranges. Default empty, which lets anyone try.

The IPs allowed to connect at all, checked before anything else. Each account can add its own list as well.

### `banner`

Text. Default empty.

A message shown before login, often a legal notice.

## Identity

### `host_keys`

List of file paths. Required.

The server's private host keys. Partners save the fingerprint the first time and get warned if it changes. Generate one with `ssh-keygen -t ed25519 -N "" -f ssh_host_ed25519_key`, and don't change it casually.

## Limits

### `max_connections`

Number. Default `25`.

Open connections at once, all accounts together, per pod. Extra connections are refused straight away. About four times your normal peak is a good value.

### `max_auth_tries`

Number. Default `6`.

Wrong passwords or keys allowed in one connection before it's closed.

### `idle_timeout`

Duration. Default `15m`.

Disconnects a logged-in partner who does nothing for this long.

## Disk

### `spool_dir`

Path. Default `/var/lib/sftpgate/spool`.

Where uploads wait until complete. Change it only outside the official image. The action queue lives in `<spool_dir>/queue`.

### `min_spool_free`

Size. Default `1GiB`.

New uploads are refused when free space would drop below this, so the disk never fills halfway through an upload.

## Reload

### `reload_interval`

Duration. Default `10s`.

How often to check the config file for changes. `SIGHUP` reloads it immediately.

## Time

### `timezone`

Time zone name. Default `UTC`.

Used for `{date}` and `{time}` in keys, and for transfer schedules. For example `Asia/Kolkata`.

## throttle

Blocks IPs that keep failing to log in. After `max_failures` failed logins from one IP within `window`, that IP is blocked for `block`.

### `throttle.max_failures`

Number. Default `5`.

### `throttle.window`

Duration. Default `10m`.

### `throttle.block`

Duration. Default `15m`.

## queue

How the actions of `after_*` hooks run in the background. Each file becomes a job on disk, so jobs survive a restart.

### `queue.workers`

Number. Default `4`.

Jobs running at the same time.

### `queue.max_attempts`

Number. Default `8`.

Tries before a job gives up and moves to `queue/failed`, where an alert fires.

### `queue.backoff`

Duration. Default `30s`.

The first wait before a retry. It doubles each time, up to 1 hour.
