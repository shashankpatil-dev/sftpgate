---
title: actions
description: The five kinds of action, and every setting of each.
---

Named, reusable work on files. Define each action once under the top-level `actions:`, and run it by name from [hooks](../hooks/). Each action is exactly one kind.

The original file is never changed: actions work on a copy and write new files.

```yaml
actions:
  recon-csv:
    check: { csv: { header: [id, amount], min_rows: 1 } }
  decrypt:
    pgp_decrypt: { key: "${file:/secrets/private.asc}" }
  warehouse:
    copy: { storage: warehouse, key: "{account}/{date}/{filename}" }
  notify:
    webhook: { url: "https://recon.internal/hooks/sftpgate", secret: "${env:HOOK_SECRET}" }
```

## `enabled`

Yes or no, on any action. Default on.

`enabled: false` keeps the action in the file, still validated, but every hook skips it, including jobs already in the queue. `pgp_encrypt` can't be switched off; remove it from the hook instead.

```yaml
actions:
  notify:
    enabled: false
    webhook: { url: "https://recon.internal/hooks/sftpgate" }
```

## `check`

Refuses a file that doesn't look right. It only reads the file, and needs at least one rule. See the [guide](../../guides/check-files/).

### `check.min_size`

Size. Files below this are refused.

### `check.max_size`

Size. Files above this are refused.

### `check.csv.header`

List. The first row must be exactly these columns. A spreadsheet byte-order mark is ignored.

### `check.csv.min_rows`

Number. At least this many rows after the first one.

### `check.csv.delimiter`

One character. Default `,`.

With `csv`, every row must have as many columns as the first one.

## `pgp_decrypt`

Decrypts with your private key. Handles armored or binary files, and strips `.pgp`, `.gpg` or `.asc` from the name. See the [guide](../../guides/pgp/).

### `pgp_decrypt.key`

Reference. Required. Your armored PGP private key.

### `pgp_decrypt.passphrase`

Reference. The key's passphrase, if it has one.

### `pgp_decrypt.signer_keys`

List. Partner public keys. When set, files must be signed by one of them.

## `pgp_encrypt`

Encrypts for recipients, and adds `.pgp` to the name.

### `pgp_encrypt.recipients`

List. Required. Armored public keys of who may read the file. A key that can't encrypt, such as a sign-only or expired key, is refused when the config loads.

## `copy`

Saves the current file to a storage. See the [guide](../../guides/copy-files/).

### `copy.storage`

Name. Required. The target storage.

### `copy.key`

Text. Required. The target name, with [placeholders](../../guides/copy-files/#key-placeholders).

## `webhook`

Tells another system about the file. See the [guide](../../guides/webhooks/).

### `webhook.url`

Text. Required. `https://...`, or `http://` inside your network.

### `webhook.secret`

Reference. A shared secret. The body is signed in `X-Sftpgate-Signature: sha256=<hex>`.

### `webhook.headers`

Map. Extra headers, for a receiver with its own auth. Headers whose name contains auth, token, key, secret or cookie must be references. `Content-Type`, `User-Agent` and `X-Sftpgate-*` can't be set.
