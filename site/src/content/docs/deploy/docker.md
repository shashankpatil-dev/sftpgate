---
title: Docker
description: Run SFTPgate on one server with Docker.
---

Best for one server, a small team, or trying it out. You need any Linux server with Docker, a [bucket you can reach](../bucket-access/), and `ssh-keygen`.

## 1. Lay out a folder

```
/opt/sftpgate/
├── config/
│   └── sftpgate.yaml          # your config, or a git checkout
├── keys/
│   └── ssh_host_ed25519_key   # the host key
└── secrets/                   # files for ${file:...} references (optional)
```

## 2. Make the host key

This is the server's identity. Make it once and keep a backup. If it changes, every partner sees a warning that looks like an attack.

```sh
ssh-keygen -t ed25519 -N "" -f /opt/sftpgate/keys/ssh_host_ed25519_key
```

Get the fingerprint to send to partners:

```sh
ssh-keygen -lf /opt/sftpgate/keys/ssh_host_ed25519_key.pub
```

The container runs as user 65532, so it must be able to read these files:

```sh
sudo chown -R 65532:65532 /opt/sftpgate/keys /opt/sftpgate/secrets
sudo chmod 600 /opt/sftpgate/keys/ssh_host_ed25519_key
```

## 3. Write a first config

Start small: one storage and one test account.

```yaml title="sftpgate.yaml"
version: 1

server:
  host_keys: [/etc/sftpgate/keys/ssh_host_ed25519_key]
  timezone: Asia/Kolkata

storages:
  main: "gs://acme-sftp-exchange"

accounts:
  - name: test-partner
    description: Our own test login
    auth:
      public_keys:
        - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl you@laptop"
    folders:
      - path: /inbound
        storage: main
        prefix: test-partner/inbound/
        perms: [list, upload]
      - path: /outbound
        storage: main
        prefix: test-partner/outbound/
        perms: [list, download]
```

Check it, using the image:

```sh
docker run --rm -v /opt/sftpgate/config:/c:ro \
  ghcr.io/shashankpatil-dev/sftpgate:0.4.0 validate -offline /c/sftpgate.yaml
```

## 4. Run it

```sh
docker run -d --name sftpgate --restart unless-stopped \
  --read-only --tmpfs /tmp \
  -p 22:2222 -p 127.0.0.1:8080:8080 \
  -v /opt/sftpgate/config:/etc/sftpgate/config:ro \
  -v /opt/sftpgate/keys:/etc/sftpgate/keys:ro \
  -v /opt/sftpgate/secrets:/etc/sftpgate/secrets:ro \
  -v sftpgate-data:/var/lib/sftpgate \
  -e SFTPGATE_CONFIG=/etc/sftpgate/config/sftpgate.yaml \
  --env-file /opt/sftpgate/secrets.env \
  ghcr.io/shashankpatil-dev/sftpgate:0.4.0
```

| Part | Why |
|---|---|
| `-p 22:2222` | Partners connect on the normal SFTP port 22. If the host's own SSH uses 22, pick another, like `-p 2222:2222`. |
| `-p 127.0.0.1:8080:8080` | Metrics, health checks and the API, on the server itself only. Put a firewall or VPN in front before opening it further. |
| `-v .../config:...:ro` | The config folder, read-only. |
| `-v .../keys:...:ro` | The host key. |
| `-v sftpgate-data:/var/lib/sftpgate` | The spool and the action queue. Keep it, or queued work is lost. |
| `--read-only --tmpfs /tmp` | Locks the container's own files and gives it a scratch `/tmp`. |
| `--env-file secrets.env` | Secrets for `${env:...}` references, one `NAME=value` per line, readable by root only. |

:::caution[Mount the folder, not the file]
Editors and git replace files instead of changing them, so a single-file mount keeps showing the old file and hot reload quietly stops working.
:::

For Compose, put the same settings in a `docker-compose.yml`. The repository's [`deploy/compose/docker-compose.yml`](https://github.com/shashankpatil-dev/sftpgate/blob/main/deploy/compose/docker-compose.yml) is a working example that also starts Prometheus, Grafana and Jaeger.

## 5. Check it's healthy

```sh
docker ps                      # STATUS shows (healthy)
docker logs sftpgate | head    # "sftpgate started"
curl -s localhost:8080/readyz  # ok
```

Then connect:

```sh
sftp -i your_private_key test-partner@your-server
```

## Update to a new version

Read the [upgrade guide](../../guides/upgrade/) first.

```sh
docker pull ghcr.io/shashankpatil-dev/sftpgate:<new-version>
docker stop sftpgate     # waits up to 30s for uploads to finish
docker rm sftpgate
# run the same docker run command with the new version
```

## Next steps

- [Give it access to your buckets](../bucket-access/).
- [Run the config from git](../config-from-git/).
- [Add a partner](../../guides/add-a-partner/).
