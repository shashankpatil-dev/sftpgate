---
title: Secrets
description: How to give SFTPgate passwords, keys and tokens without putting them in git.
---

No secret values go in `sftpgate.yaml`. Secret fields must be a reference, and `validate` refuses anything else. That keeps the file safe to store in git and review in pull requests.

## The three kinds of reference

```yaml
password_hash: "${env:PAYGATE_PW_HASH}"                       # environment variable
key: "${file:/etc/sftpgate/secrets/recon/private.asc}"        # a file
password_hash: "${secret:gcpsecretmanager://projects/acme/secrets/paygate-pw-hash}"
password_hash: "${secret:awssecretsmanager://paygate-pw-hash?region=ap-south-1}"
password_hash: "${secret:awsparamstore://paygate-pw-hash?region=ap-south-1}"
```

| Reference | Best for | Docker | Helm | After rotating |
|---|---|---|---|---|
| `env` | Short values | `--env-file` | `secretEnv` | Restart |
| `file` | Keys (multi-line) | Mount a folder | `secretFiles` | Picked up by itself |
| `secret` | Reading straight from a cloud secret store, with the same identity as the buckets | Nothing extra | Nothing extra | Send `SIGHUP` or restart |

Every reference is read again on reload.

## Which settings are secret

| Setting | Secret? | How to give it |
|---|---|---|
| `auth.public_keys` | No | Directly, or a reference |
| `auth.password_hash` | Yes: a leaked hash can be cracked offline | Reference only |
| `server.host_keys` | Yes | A file path (mounted secret) |
| `pgp_decrypt.key`, `pgp_decrypt.passphrase` | Yes | Reference only |
| `pgp_decrypt.signer_keys`, `pgp_encrypt.recipients` | No: public keys | Directly, or a reference |
| `webhook.secret` | Yes | Reference only |
| `webhook.headers` that carry auth | Yes | Reference only |
| `http.tokens`, `http.admin.tokens` | Yes: at least 16 characters | Reference only |
| `remotes.*.private_key`, `remotes.*.password` | Yes | Reference only |
| `remotes.*.host_key` | No | Directly, or a reference |

## Partner passwords

Hash the password and store the hash in your secret store. The partner keeps the password.

```sh
sftpgate hash-password            # asks twice, prints $2a$12$...
echo 'pw' | sftpgate hash-password -cost 12
```

## API and admin tokens

```sh
openssl rand -hex 24
```

Store the token as a secret and list it under [`http.tokens`](../../reference/http/#tokens). Several tokens are allowed, for example one per team, so you can change one without cutting everyone off.

Admin tokens are made the same way but kept separate. They must differ from every read-only token, and work only as a Bearer header. See [`http.admin`](../../reference/http/#admin).
