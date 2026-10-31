---
title: storages
description: Name your buckets, for GCS, S3, Azure, S3-compatible stores and local disk.
---

A map of short names to bucket URLs. Folders, copies and transfers refer to storages by these names. Names use lowercase letters, numbers and `-`.

```yaml
storages:
  gcs-bucket:   "gs://acme-recon-raw"
  s3-bucket:    "s3://acme-reports?region=ap-south-1"
  minio-bucket: "s3://exchange?endpoint=https://minio.internal:9000&use_path_style=true&region=us-east-1"
  azure:        "azblob://exchange"
  local:        "file:///var/lib/sftpgate/data"
```

Credentials never go here. See [bucket access](../../deploy/bucket-access/).

## URL forms

| Store | URL |
|---|---|
| Google Cloud Storage | `gs://<bucket>` |
| Amazon S3 | `s3://<bucket>?region=<region>` |
| S3-compatible (MinIO, R2, Ceph) | `s3://<bucket>?endpoint=<url>&region=<region>&use_path_style=true` |
| Azure Blob | `azblob://<container>`, with `AZURE_STORAGE_ACCOUNT` set |
| Local disk | `file:///<path>`, for trying things out |

## How files are stored

Each object carries its SHA-256, the account, IP and session as metadata. After every save, SFTPgate reads the object's size and checksum back. GCS and Azure also check an MD5 while saving, and S3 checks each request with its own checksums.
