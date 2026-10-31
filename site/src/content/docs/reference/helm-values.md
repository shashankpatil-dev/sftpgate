---
title: Helm values
description: Every setting of the Helm chart.
---

Set these in your values file. The chart's [`values.yaml`](https://github.com/shashankpatil-dev/sftpgate/blob/main/deploy/helm/sftpgate/values.yaml) has comments on each.

## Image

| Value | Default | What it does |
|---|---|---|
| `image.repository` | `ghcr.io/shashankpatil-dev/sftpgate` | Which image to run |
| `image.tag` | Chart version | Which version |
| `image.pullPolicy` | `IfNotPresent` | When to pull it |
| `imagePullSecrets` | `[]` | For a private registry |

## Scale

| Value | Default | What it does |
|---|---|---|
| `replicas` | `1` | How many copies. With more than 1, transfers run on the first only. Ignored when autoscaling is on. |
| `autoscaling.enabled` | Off | A HorizontalPodAutoscaler on CPU. It scales down slowly, one pod per 5 minutes, because removing a pod disconnects its partners. |
| `autoscaling.minReplicas` | `2` | Fewest copies |
| `autoscaling.maxReplicas` | `4` | Most copies |
| `autoscaling.targetCPUUtilizationPercentage` | `70` | CPU target |
| `podDisruptionBudget.enabled` | Off | Keeps pods up during cluster maintenance |
| `podDisruptionBudget.minAvailable` | `1` | Pods to keep |
| `resources` | 100m CPU, 128Mi requested, 512Mi limit | Pod size |
| `terminationGracePeriodSeconds` | `45` | Time to finish uploads on shutdown |

## Config and secrets

| Value | Default | What it does |
|---|---|---|
| `config` | A minimal config | The full `sftpgate.yaml` |
| `existingConfigMap` | Empty | Your own ConfigMap (key `sftpgate.yaml`) instead of `config` |
| `hostKeys.existingSecret` | `sftpgate-host-keys` | Secret with the host key files |
| `secretEnv` | `[]` | Secret names loaded as env vars, for `${env:}` |
| `secretFiles` | `[]` | `{secretName, mountPath}` pairs mounted as files, for `${file:}` |
| `env` | `[]` | Extra env vars, such as tracing or cloud settings |

## Network

| Value | Default | What it does |
|---|---|---|
| `service.sftp.type` | `LoadBalancer` | How partners reach it. `NodePort` and `ClusterIP` also work. |
| `service.sftp.port` | `22` | The public port |
| `service.sftp.externalTrafficPolicy` | `Local` | Keeps partners' real IPs for allowlists |
| `service.sftp.loadBalancerSourceRanges` | `[]` | Cloud firewall: who may reach the port at all |
| `service.sftp.annotations` | `{}` | Cloud load balancer options, such as an internal LB or static IP |
| `service.http.port` | `8080` | The internal metrics, health and API service |
| `service.http.annotations` | `{}` | Annotations for that service |
| `ingress.enabled` | Off | Web access to the API, and admin calls if allowed. Keep it on an internal network. |
| `ingress.className`, `host`, `annotations`, `tls` | Empty | Standard Ingress settings |

## Disk

| Value | Default | What it does |
|---|---|---|
| `persistence.enabled` | `true` | A disk per pod for the spool and queue. Without it, a pod restart loses queued actions. |
| `persistence.size` | `2Gi` | Largest `max_file_size` × uploads at the same time, plus room for actions |
| `persistence.storageClass` | Cluster default | Which disk type |
| `persistence.accessModes` | `[ReadWriteOnce]` | How it's mounted |

## Config from git

| Value | Default | What it does |
|---|---|---|
| `gitSync.enabled` | Off | [Config from git](../../deploy/config-from-git/) |
| `gitSync.repo` | Empty | The repository |
| `gitSync.ref` | `main` | The branch |
| `gitSync.path` | `sftpgate.yaml` | The file in the repository |
| `gitSync.period` | `30s` | How often to check |
| `gitSync.extraEnv` | `[]` | git-sync credentials for private repositories |
| `gitSync.image` | `registry.k8s.io/git-sync/git-sync:v4.4.2` | git-sync version |

## Identity

| Value | Default | What it does |
|---|---|---|
| `serviceAccount.create` | `true` | Creates the ServiceAccount |
| `serviceAccount.name` | Generated | Its name |
| `serviceAccount.annotations` | `{}` | Workload identity annotations go here. See [bucket access](../../deploy/bucket-access/). |

## Metrics

These need the Prometheus Operator, or a Grafana sidecar for the dashboard.

| Value | Default | What it does |
|---|---|---|
| `metrics.serviceMonitor.enabled` | Off | Prometheus scraping |
| `metrics.serviceMonitor.interval` | `30s` | How often |
| `metrics.serviceMonitor.labels` | `{}` | Labels your Prometheus selects on |
| `metrics.prometheusRule.enabled` | Off | The 11 [alert rules](../../operate/alerts/) |
| `metrics.prometheusRule.labels` | `{}` | Labels your Prometheus selects on |
| `metrics.grafanaDashboard.enabled` | Off | The dashboard, as a ConfigMap |
| `metrics.grafanaDashboard.labels` | `grafana_dashboard: "1"` | Labels your Grafana sidecar selects on |

## Placement

| Value | Default | What it does |
|---|---|---|
| `podAnnotations`, `podLabels` | Empty | Standard pod metadata |
| `nodeSelector`, `tolerations`, `affinity` | Empty | Standard pod placement |
| `topologySpreadConstraints` | Empty | Spreads pods across zones |
| `priorityClassName` | Empty | Gives SFTPgate scheduling priority |
| `extraVolumes`, `extraVolumeMounts` | Empty | Mounts anything else, such as a corporate CA bundle for a private S3 endpoint |
