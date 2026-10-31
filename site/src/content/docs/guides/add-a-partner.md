---
title: Add a partner
description: The everyday task, from asking for a key to the first upload.
---

## 1. Ask the partner

- Their **public key**: one line starting with `ssh-ed25519` or `ssh-rsa`.
- Their **IP addresses** or ranges.
- For two-step login, a password. Hash it right away with `echo 'their-password' | sftpgate hash-password`.

:::danger
Never ask for or accept a partner's private key.
:::

## 2. Add the account

Open a pull request that adds this to `accounts` in `sftpgate.yaml`:

```yaml
- name: newvendor
  description: Newvendor daily stock files
  auth:
    public_keys: ["ssh-ed25519 AAAA... newvendor-prod"]
  allow_ips: ["198.51.100.0/24"]
  max_sessions: 3
  folders:
    - path: /inbound
      storage: main
      prefix: newvendor/inbound/
      perms: [list, upload]
      patterns: ["*.csv"]
      max_file_size: 200MiB
```

If you used a password, add its hash to your secret store as `NEWVENDOR_PW_HASH` and reference it:

```yaml
password_hash: "${env:NEWVENDOR_PW_HASH}"
```

## 3. Review and merge

CI runs `sftpgate validate -offline`, a reviewer approves, and you merge. With [config from git](../../deploy/config-from-git/), it's live within 30 seconds.

## 4. Send the partner their details

- The address and port.
- Their username (`newvendor`).
- The host key fingerprint.

If they use checks, tell them a refused upload means "fix the file and send it again". See [what partners see](../check-files/#what-a-partner-sees).

## 5. Check the first login

Look for their first `session_start` and `upload` in the audit log, or in the API:

```sh
curl -H "Authorization: Bearer $TOKEN" \
  "localhost:8080/api/v1/events?account=newvendor"
```

## Common folder setups

```yaml
# Partner sends files to us
- { path: /inbound,  storage: main, prefix: p/in/,   perms: [list, upload] }
# Partner fetches files we prepared
- { path: /outbound, storage: main, prefix: p/out/,  perms: [list, download, delete] }
# Partner may also fix a wrong upload
- { path: /drop,     storage: main, prefix: p/drop/, perms: [list, upload, overwrite, rename] }
```

## Remove a partner

Delete their account block and merge. Open sessions are closed on the next reload.
