---
title: Kubernetes
description: Run SFTPgate on any Kubernetes cluster with Helm.
---

Best when partners depend on it: it restarts itself, survives machine failures, can run several copies, and plugs into cluster monitoring. You need a cluster and Helm 3.

The chart runs a StatefulSet, so each pod gets its own disk for the spool and the action queue, behind a LoadBalancer that keeps partners' real IPs.

## 1. Create the namespace and host key

```sh
ssh-keygen -t ed25519 -N "" -f ssh_host_ed25519_key
kubectl create namespace sftpgate
kubectl -n sftpgate create secret generic sftpgate-host-keys \
  --from-file=ssh_host_ed25519_key
```

Keep a backup of the key. If it changes, every partner sees a warning that looks like an attack.

## 2. Create the secrets

One Secret holds values used as `${env:NAME}`:

```sh
kubectl -n sftpgate create secret generic sftpgate-secrets \
  --from-literal=PAYGATE_PW_HASH='$2a$12$...' \
  --from-literal=SFTPGATE_API_TOKEN="$(openssl rand -hex 24)"
```

Other Secrets hold files used as `${file:...}`, such as PGP keys:

```sh
kubectl -n sftpgate create secret generic recon-pgp \
  --from-file=private.asc --from-file=paygate-signing.pub.asc
```

In a GitHub Actions deploy job, the same commands read from GitHub secrets. With External Secrets Operator, Vault or a cloud secret manager, create the same Secrets with those tools instead.

## 3. Write your values file

```yaml title="my-values.yaml"
replicas: 2

# The config. With gitSync this part is not used.
config: |
  version: 1
  server:
    host_keys: [/etc/sftpgate/keys/ssh_host_ed25519_key]
    timezone: Asia/Kolkata
  http:
    tokens: ["${env:SFTPGATE_API_TOKEN}"]
  storages:
    main: "gs://acme-sftp-exchange"
  actions:
    decrypt-paygate:
      pgp_decrypt:
        key: "${file:/etc/sftpgate/secrets/recon/private.asc}"
        signer_keys: ["${file:/etc/sftpgate/secrets/recon/paygate-signing.pub.asc}"]
  accounts:
    - name: paygate
      auth:
        mode: all
        public_keys: ["ssh-ed25519 AAAA... paygate"]
        password_hash: "${env:PAYGATE_PW_HASH}"
      allow_ips: ["52.66.0.0/16"]
      folders:
        - path: /inbound
          storage: main
          prefix: paygate/inbound/
          perms: [list, upload]
          hooks:
            after_upload: [decrypt-paygate]

# Secrets as env vars, for ${env:...}
secretEnv: [sftpgate-secrets]

# Secrets as files, for ${file:...}
secretFiles:
  - secretName: recon-pgp
    mountPath: /etc/sftpgate/secrets/recon

# Bucket access without keys
serviceAccount:
  annotations:
    iam.gke.io/gcp-service-account: sftpgate@acme-prod.iam.gserviceaccount.com

service:
  sftp:
    # Only these networks can even reach the port (cloud firewall).
    loadBalancerSourceRanges: ["52.66.0.0/16", "203.0.113.0/24"]

persistence:
  size: 5Gi

metrics:
  serviceMonitor: { enabled: true }
  prometheusRule: { enabled: true }
  grafanaDashboard: { enabled: true }
```

See [bucket access](../bucket-access/) for AWS and Azure, and [Helm values](../../reference/helm-values/) for every setting.

## 4. Install

The chart lives in the repository, so clone it first.

```sh
git clone https://github.com/shashankpatil-dev/sftpgate.git
helm install sftpgate sftpgate/deploy/helm/sftpgate -n sftpgate -f my-values.yaml
kubectl -n sftpgate rollout status statefulset/sftpgate
kubectl -n sftpgate get service sftpgate    # EXTERNAL-IP is the partner address
```

Point a DNS name like `sftp.acme.com` at that address. Send partners the name, the port (22), and the host key fingerprint. The install notes print the command for it.

## Make changes

| Change | How |
|---|---|
| Config | Edit `my-values.yaml` and run `helm upgrade`. It's picked up within about a minute, without a restart. |
| Secret in a file | Update the Secret. It's picked up by itself. |
| Secret in an env var | Update the Secret, then `kubectl -n sftpgate rollout restart statefulset/sftpgate`. |
| Host key | Update the Secret, restart as above, and warn partners. |
| SFTPgate version | `helm upgrade ... --set image.tag=<version>`. Pods are replaced one by one, and uploads in progress get 30 seconds to finish. |

## Run several copies

With `replicas` above 1, partners are spread across pods and scheduled transfers run on the first pod only, so each file moves once. Each pod keeps its own action queue.

## Without Helm

[`deploy/kubernetes/`](https://github.com/shashankpatil-dev/sftpgate/tree/main/deploy/kubernetes) has the same setup as plain manifests, with instructions.

## Next steps

- [Run the config from git](../config-from-git/).
- [Add a partner](../../guides/add-a-partner/).
- [Size it](../../operate/sizing/).
