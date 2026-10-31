---
title: remotes
description: Other SFTP servers that SFTPgate connects to.
---

A map of short names to partner SFTP servers, used by [transfers](../transfers/).

```yaml
remotes:
  bank:
    addr: sftp.bank.example:22
    user: acme
    host_key: "ssh-ed25519 AAAA..."
    private_key: "${file:/etc/sftpgate/secrets/bank_id_ed25519}"
```

## `addr`

Text. Required.

`host:port`, like `sftp.bank.example:22`.

## `user`

Text. Required.

The username **you** were given on their server.

## `host_key`

Text. Required.

**Their** server's public host key, pinned. If the server ever shows another key, SFTPgate refuses to connect, which protects you from fake servers. Ask them for it, or run `ssh-keyscan -t ed25519 sftp.bank.example` and confirm the fingerprint with them.

## `private_key`

Reference.

Your private key for their server. Give them the public half.

## `password`

Reference.

Your password for their server. At least one of `private_key` or `password` is needed.
