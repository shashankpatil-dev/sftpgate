---
title: Alerts
description: The 11 ready-made Prometheus alert rules.
---

The repository includes [11 alert rules](https://github.com/shashankpatil-dev/sftpgate/blob/main/deploy/helm/sftpgate/files/alerts.yml). On Kubernetes, install them with `metrics.prometheusRule.enabled: true`. Elsewhere, copy the file into your Prometheus.

Prometheus shows them as firing. To get messages, [connect Alertmanager](../../guides/alerts-to-slack/).

| Alert | Tells you | What to do |
|---|---|---|
| `SftpgateDown` | SFTPgate isn't answering on `/metrics` | Check the pod or container |
| `SftpgateNotReady` | It's running but has no config | Check the logs for the config error |
| `SftpgateLoginFailureSpike` | More than 20 failed logins in 5 minutes | Look at `auth_failed` events. Someone may be guessing. |
| `SftpgateIPsBlocked` | IPs are blocked after failed logins | See if it's an attacker, or a partner with a wrong key |
| `SftpgateStorageErrors` | A bucket is failing | Check the bucket and [access to it](../../deploy/bucket-access/) |
| `SftpgateConfigReloadFailed` | A new config was rejected | Fix the file. The old config is still running. |
| `SftpgateSpoolDiskLow` | Less than 2 GiB free on the upload disk | Add disk, or lower `max_file_size` |
| `SftpgateActionsRetrying` | Actions are failing and being retried | Check `action` lines in the audit log |
| `SftpgateActionsGaveUp` | An action job gave up | The file is in the bucket but wasn't processed. Fix the cause, then [retry](../api/#retry-failed-action-jobs). |
| `SftpgateActionsNotQueued` | A file was saved, but its actions couldn't be queued | The spool disk is full or not writable |
| `SftpgateTransferFailing` | A scheduled transfer failed in the last hour | Check `transfer` events |
