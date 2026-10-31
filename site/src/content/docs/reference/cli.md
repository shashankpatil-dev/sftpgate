---
title: Command line
description: The commands of the sftpgate program.
---

Build it with `make build`, or run any command from the image:

```sh
docker run --rm ghcr.io/shashankpatil-dev/sftpgate:0.4.0 version
```

## `serve`

Starts the server. This is what the container runs.

| Flag | Default | What it does |
|---|---|---|
| `-config` | `SFTPGATE_CONFIG`, or `/etc/sftpgate/sftpgate.yaml` | The config file |

## `validate`

Checks a config file and prints every problem it finds.

```sh
sftpgate validate -offline sftpgate.yaml
```

| Flag | What it does |
|---|---|
| `-offline` | Skips checks that need secrets, files or the network. Use it in CI. |

## `hash-password`

Asks for a password and prints its bcrypt hash. In a terminal it asks twice, without showing what's typed. Piped input is read from the first line, for scripts.

| Flag | Default | What it does |
|---|---|---|
| `-cost` | `12` | bcrypt cost, 10 or more |

## `healthcheck`

Asks the running server if it's alive. The image has no shell or curl, so Docker's health check runs this.

| Flag | Default | What it does |
|---|---|---|
| `-url` | `http://127.0.0.1:8080/healthz` | The health URL to check |

## `version`

Prints the version.
