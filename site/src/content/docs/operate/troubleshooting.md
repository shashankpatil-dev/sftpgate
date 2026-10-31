---
title: Troubleshooting
description: Common problems, their causes and fixes.
---

## Check that it works

| Check | How |
|---|---|
| Config is valid | `sftpgate validate -offline sftpgate.yaml` |
| Process is alive | `curl localhost:8080/healthz`, or `docker ps` shows healthy |
| Buckets are reachable | `curl localhost:8080/readyz` |
| A partner can log in | `sftp -i key -P 22 account@host` |
| What's happening | Grafana, `GET /api/v1/events`, or `docker logs` and `kubectl logs` |
| Which config is live | `curl -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/status` |
| Metrics | `curl localhost:8080/metrics` |

## Config errors

| You see | Likely cause | Fix |
|---|---|---|
| `inline secret not allowed` | A secret is written into the config | Use `${env:}`, `${file:}` or `${secret:}` |
| `env X not set` | The reference points to a missing variable | Add it to `--env-file` or `secretEnv` |
| `possible plaintext password` | `password_hash` isn't a bcrypt hash | Use `sftpgate hash-password` |
| Edits aren't picked up in Docker | The config file is mounted instead of its folder | Mount the folder |

## Partners can't connect

| You see | Likely cause | Fix |
|---|---|---|
| "Permission denied (publickey)" | Wrong key, wrong username, or IP not allowed | Check the audit log: `auth_failed` shows the user tried |
| The connection closes at once | IP blocked by the allowlist or throttle | Look for `conn_rejected` and its reason |
| "Host key verification failed" | The host key changed, or they saved the wrong one | Restore the original key, and send the fingerprint again |
| Very old software can't connect | It only supports SHA-1 | The partner must upgrade |

## Uploads are refused

| You see | Likely cause | Fix |
|---|---|---|
| "out of upload space" | The spool disk is too small for `max_file_size` × uploads at once | Increase `persistence.size`, or lower `max_file_size` |
| "file already exists" | The folder has no `overwrite` | Add `overwrite`, or have the partner use a new name |
| `Failure` at the end of an upload | A check refused the file, or the storage failed | Find the `upload` event in the audit log for the reason. See [what partners see](../../guides/check-files/#what-a-partner-sees). |

## The service isn't ready

| You see | Likely cause | Fix |
|---|---|---|
| `/readyz` fails | A bucket can't be reached, or there's no permission | Check [bucket access](../../deploy/bucket-access/) |
