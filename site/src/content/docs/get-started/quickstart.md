---
title: Quickstart
description: Run SFTPgate on your laptop and send your first file in five minutes.
---

You will run SFTPgate in Docker, log in as a test partner, and upload a file. A folder on your disk stands in for the cloud bucket.

You need Docker and the OpenSSH `sftp` command. Linux and macOS have both ready; on Windows, use WSL.

## 1. Make the keys

Make a folder to work in, then create two key pairs: one for the server and one for your test partner.

```sh
mkdir sftpgate-demo && cd sftpgate-demo
mkdir config keys data data/bucket
ssh-keygen -q -t ed25519 -N "" -f keys/host_key
ssh-keygen -q -t ed25519 -N "" -f keys/partner_key
```

## 2. Write the config

This creates one storage and one account with one folder, and puts the partner's public key into the file.

```sh
cat > config/sftpgate.yaml <<EOF
version: 1

server:
  host_keys: [/keys/host_key]
  spool_dir: /data/spool

storages:
  main: "file:///data/bucket"

accounts:
  - name: acme
    auth:
      public_keys: ["$(cat keys/partner_key.pub)"]
    folders:
      - path: /inbound
        storage: main
        prefix: acme/
        perms: [list, upload, download]
EOF
```

## 3. Start SFTPgate

```sh
docker run -d --name sftpgate \
  --user "$(id -u):$(id -g)" \
  -p 2222:2222 -p 8080:8080 \
  -v "$PWD/config:/config:ro" \
  -v "$PWD/keys:/keys:ro" \
  -v "$PWD/data:/data" \
  -e SFTPGATE_CONFIG=/config/sftpgate.yaml \
  ghcr.io/shashankpatil-dev/sftpgate:0.4.0
```

Check it's ready. This prints `ok`:

```sh
curl localhost:8080/readyz
```

## 4. Send a file

Log in as the partner. Answer `yes` when asked about the host key.

```sh
echo "id,amount" > report.csv
sftp -P 2222 -i keys/partner_key acme@localhost
```

Then upload the file:

```
sftp> put report.csv /inbound/report.csv
sftp> ls -l /inbound
sftp> bye
```

## 5. See what happened

The file landed under the `acme/` prefix. The `.attrs` file beside it is how local storage keeps metadata such as the SHA-256; cloud buckets store that on the object itself.

```sh
ls data/bucket/acme
```

The audit log has a line for the login and the upload, with the file's checksum:

```sh
docker logs sftpgate 2>&1 | grep '"event":"upload"'
```

## 6. Try hot reload

Open `config/sftpgate.yaml`, remove `download` from `perms`, and save. Within 10 seconds SFTPgate reloads the file without a restart. Log in again and try `get /inbound/report.csv`: it's refused.

Now type something invalid in the file, such as `perms: [fly]`. `docker logs sftpgate` shows the rejected config, and the old one keeps running.

## Clean up

```sh
docker rm -f sftpgate
cd .. && rm -rf sftpgate-demo
```

## Next steps

- [Learn the core concepts](../concepts/).
- [Deploy with Docker](../../deploy/docker/) or [Kubernetes](../../deploy/kubernetes/) with a real bucket.
- [Add a real partner](../../guides/add-a-partner/).
