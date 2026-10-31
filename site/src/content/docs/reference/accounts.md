---
title: accounts
description: Logins, how they authenticate, and from where.
---

A list. Each item is one SFTP login.

```yaml
accounts:
  - name: paygate
    description: Daily settlement files
    auth:
      mode: all
      public_keys: ["ssh-ed25519 AAAA... paygate-prod"]
      password_hash: "${env:PAYGATE_PW_HASH}"
    allow_ips: ["52.66.0.0/16", "13.232.10.4"]
    max_sessions: 5
    folders:
      - path: /inbound
        storage: recon-raw
        prefix: paygate/
        perms: [list, upload]
```

## `name`

Text. Required.

The SFTP username: lowercase letters, numbers, `.`, `_` and `-`.

## `description`

Text. Default empty.

A note for people. Partners never see it.

## `auth`

### `auth.mode`

`any` or `all`. Default `any`.

- `any`: a valid key **or** a valid password is enough. Needs at least one of them set.
- `all`: the partner needs their key **and** their password. Needs both set.

:::caution[Paramiko can't do `all`]
OpenSSH, PuTTY (and so WinSCP and FileZilla) and Go clients work with `mode: all`. Paramiko can't do the two-step login against SFTPgate, so give Paramiko partners `mode: any` with a key.
:::

### `auth.public_keys`

List. Default empty.

The partner's public keys, one line each. ed25519 or RSA with SHA-2. These aren't secret, so they can be written directly.

### `auth.password_hash`

Reference. Default empty.

A reference to a bcrypt hash, never the password itself. Make one with `sftpgate hash-password`.

## `allow_ips`

List of IP ranges. Default empty, which allows any IP the server allows.

The IPs this account may log in from, on top of `server.allow_ips`.

## `max_sessions`

Number. Default `0`, which means no limit.

Connections this account may have open at once. `server.max_connections` still applies.

## `folders`

List. At least one is required.

What the account sees. See [folders](../folders/).
