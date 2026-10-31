---
title: Send alerts to Slack
description: Route SFTPgate's alerts to Slack, email or PagerDuty with Alertmanager.
---

Alerts about the service itself, such as a failing transfer or a spike of failed logins, come from Prometheus rules. Prometheus only shows them as firing. To notify people, connect it to Alertmanager.

On Kubernetes, the Prometheus Operator usually has an Alertmanager already. Set `metrics.prometheusRule.enabled: true` in your Helm values and add a route.

## Add a route

```yaml title="alertmanager.yml"
route:
  receiver: sftp-team
  routes:
    - matchers: [alertname=~"Sftpgate.*"]
      receiver: sftp-team
receivers:
  - name: sftp-team
    slack_configs:
      - api_url_file: /etc/alertmanager/slack-webhook-url
        channel: "#sftp-alerts"
        send_resolved: true
```

Alertmanager handles grouping, silencing and repeats, and can send to email or PagerDuty the same way.

See [every alert](../../operate/alerts/).

## Missing files

SFTPgate doesn't alert when an expected file never arrives. Nothing fails in that case, and a pull that finds no files is a normal run. Use your bucket's monitoring, or a scheduled job that checks the bucket.
