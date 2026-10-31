---
title: Decrypt and encrypt with PGP
description: Replace decryption scripts, and send files only the recipient can open.
---

## Decrypt what partners send

Give the partner your public key. Keep your private key as a secret, and reference it.

```yaml
actions:
  decrypt-paygate:
    pgp_decrypt:
      key: "${file:/etc/sftpgate/secrets/recon-private.asc}"
      passphrase: "${env:RECON_PGP_PASSPHRASE}"
      signer_keys: ["${file:/etc/sftpgate/secrets/paygate-signing.pub.asc}"]
```

`signer_keys` is optional. When set, a file must be signed by one of those keys, which proves it really came from that partner. Unsigned or forged files are rejected.

Decrypting strips `.pgp`, `.gpg` or `.asc` from the name. The next actions in the hook get the plain file, so you usually copy it somewhere:

```yaml
actions:
  plain-copy:
    copy: { storage: recon-plain, key: "{account}/{date}/{filename}" }

# in the folder
hooks:
  after_upload: [decrypt-paygate, plain-copy]
```

:::caution
The original file is never changed, because actions only write new files. Without a `copy` after the decrypt, the plain file goes nowhere.
:::

## Encrypt before sending

```yaml
actions:
  encrypt-for-bank:
    pgp_encrypt:
      recipients: ["${file:/etc/sftpgate/secrets/bank.pub.asc}"]
```

In `before_push`, the partner receives the encrypted file, named `<name>.pgp`:

```yaml
hooks:
  before_push: [settlement-csv, encrypt-for-bank]
```

A key that can't encrypt, such as a sign-only or expired key, is refused when the config loads, not at run time.

:::note
`pgp_encrypt` can't be switched off with `enabled: false`, so files are never quietly sent unencrypted. To stop encrypting, remove it from the hook, where reviewers will see it.
:::
