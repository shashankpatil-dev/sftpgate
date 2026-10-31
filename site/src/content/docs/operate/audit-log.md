---
title: Audit log
description: One JSON line for every security-relevant event.
---

The audit log answers "who downloaded this file?". SFTPgate writes it to standard output, and your log system (Loki, Cloud Logging, ELK, Datadog) collects and keeps it. Send it to a retention-locked bucket and it can't be altered.

```json
{"time":"2026-10-31T07:16:10Z","level":"INFO","msg":"audit","event":"upload","result":"ok","session":"ba4641e4d3ccecd6","account":"acme","ip":"172.17.0.1","path":"/inbound/report.csv","storage":"main","key":"acme/report.csv","sha256":"5891b5b5...","size":6,"duration_ms":1}
```

## Events

| Event | When |
|---|---|
| `conn_rejected` | A connection is refused before login: IP, throttle or connection limit |
| `auth_failed` | A login fails |
| `session_start`, `session_end` | A partner logs in or disconnects |
| `upload`, `download`, `delete`, `rename`, `mkdir`, `rmdir` | File operations |
| `action` | An action job finished or failed |
| `transfer` | A scheduled transfer moved a file or failed |
| `config_reload` | A new config was accepted or rejected |
| `admin` | An admin call, allowed or refused |

## Results

| Result | Meaning |
|---|---|
| `ok` | It worked |
| `denied` | Refused by the rules |
| `failed` | Something broke |

## Fields

Fields appear when they apply.

| Field | What it holds |
|---|---|
| `time`, `event`, `result` | When, what, and how it ended |
| `error` | The reason, when it was refused or failed |
| `session` | The SFTP session id |
| `account`, `user` | The account, and the username tried |
| `ip` | The client's address |
| `method`, `key_fp` | How they logged in, and the key's fingerprint |
| `path`, `to_path` | The path the partner used |
| `storage`, `key`, `to_key` | Where the file is in the bucket |
| `sha256`, `size` | The file's checksum and size |
| `duration_ms` | How long it took |
| `config` | The hash of the config in force |
| `trace_id` | The matching trace, when tracing is on |
