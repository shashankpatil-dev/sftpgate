---
title: Config from git
description: Make a merged pull request go live on its own.
---

Keep the config in a git repository and a merge goes live by itself. [git-sync](https://github.com/kubernetes/git-sync) copies the repository onto disk, and SFTPgate reloads the file.

A merged commit goes live within about 30 seconds. A commit with a broken config is rejected, and the old config keeps running. The API and the `sftpgate_config_info` metric show which commit is live.

## On Kubernetes

Add this to your values:

```yaml
gitSync:
  enabled: true
  repo: https://github.com/acme/sftpgate-config.git
  ref: main
  path: prod/sftpgate.yaml
```

For a private repository, give git-sync a token:

```yaml
gitSync:
  extraEnv:
    - name: GITSYNC_USERNAME
      value: sftpgate-bot
    - name: GITSYNC_PASSWORD
      valueFrom: { secretKeyRef: { name: git-creds, key: token } }
```

## With Docker

The simplest way is a `git pull` in `/opt/sftpgate/config` from a cron job or your deploy pipeline. SFTPgate notices the change within 10 seconds.

For automatic delivery, use git-sync as in [`deploy/compose/gitops.yml`](https://github.com/shashankpatil-dev/sftpgate/blob/main/deploy/compose/gitops.yml):

```sh
GITOPS_REPO=https://github.com/acme/sftpgate-config.git \
GITOPS_PATH=prod/sftpgate.yaml \
docker compose -f docker-compose.yml -f gitops.yml up -d
```

## Check every pull request

Run `validate` in CI, so a mistake is caught before it's merged. `-offline` skips checks that need real secrets, files or the network.

```yaml title=".github/workflows/sftpgate.yml"
on:
  pull_request:
    paths: ["prod/sftpgate.yaml"]

jobs:
  validate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: |
          docker run --rm -v "$PWD:/repo:ro" \
            ghcr.io/shashankpatil-dev/sftpgate:0.4.0 \
            validate -offline /repo/prod/sftpgate.yaml
```

The output lists every problem with its exact place, like `accounts[0].auth.mode: must be any or all`.
