---
title: Bucket access
description: Give SFTPgate permission to use your buckets, without keys in the config.
---

Credentials never go in the config. Give the SFTPgate process an identity that's allowed to use the buckets, and nothing more. It needs to read, write, list and delete objects on each bucket.

## Kubernetes

Use your cloud's workload identity, so no keys exist at all.

| Cloud | ServiceAccount annotation | Permission |
|---|---|---|
| GCS | `iam.gke.io/gcp-service-account: <gsa>@<project>.iam.gserviceaccount.com` | Storage Object Admin on the bucket |
| AWS S3 | `eks.amazonaws.com/role-arn: arn:aws:iam::<acct>:role/sftpgate` | `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject`, `s3:ListBucket` on the bucket |
| Azure Blob | `azure.workload.identity/client-id: <id>`, and set `AZURE_STORAGE_ACCOUNT` | Storage Blob Data Contributor |
| MinIO, Ceph, R2 | None. Put `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` in a Secret and use `secretEnv` | Your store's equivalent |

Set the annotation in your [Helm values](../../reference/helm-values/#identity) under `serviceAccount.annotations`.

## VM or Docker

| Cloud | How |
|---|---|
| GCS | The VM's service account, or `GOOGLE_APPLICATION_CREDENTIALS=/path/key.json` |
| AWS S3 | The instance role, or `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` |
| Azure Blob | `AZURE_STORAGE_ACCOUNT` plus `AZURE_STORAGE_KEY`, or a managed identity |
| MinIO, Ceph, R2 | `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` |

## Keep access narrow

Use a separate bucket, or at least a separate prefix, per partner or purpose, and limit SFTPgate's permissions to those buckets.

To write storage URLs, see [`storages`](../../reference/storages/).
