#!/usr/bin/env bash
# End-to-end checks against the docker compose setup, using the real
# OpenSSH sftp client. Run `make up` first.
set -euo pipefail

HOST=${HOST:-localhost}
PORT=${PORT:-2222}
HTTP=${HTTP:-http://localhost:8080}
PROMETHEUS=${PROMETHEUS:-http://localhost:9090}
GRAFANA=${GRAFANA:-http://localhost:3000}
JAEGER=${JAEGER:-http://localhost:16686}
KEY=${KEY:-.dev/keys/demo_user}
UI_TOKEN=${UI_TOKEN:-dev-only-ui-token}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

ssh_opts=(-q -i "$KEY" -P "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes)

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

echo "waiting for sftpgate to be ready..."
for _ in $(seq 1 30); do
  curl -fs "$HTTP/readyz" >/dev/null && break
  sleep 1
done
curl -fs "$HTTP/readyz" >/dev/null || fail "/readyz is not ok"

head -c 5000000 /dev/urandom > "$work/upload.bin"

for folder in s3 gcs; do
  echo "checking /$folder ..."
  sftp "${ssh_opts[@]}" -b - "demo@$HOST" > "$work/$folder.log" <<EOF
put $work/upload.bin /$folder/upload.bin
mkdir /$folder/archive
rename /$folder/upload.bin /$folder/archive/upload.bin
ls /$folder/archive
get /$folder/archive/upload.bin $work/$folder.download
rm /$folder/archive/upload.bin
rmdir /$folder/archive
EOF
  cmp -s "$work/upload.bin" "$work/$folder.download" || fail "/$folder: downloaded file differs from uploaded file"
done

echo "checking that the post-upload copy reached /gcs/copies..."
copied=""
for _ in $(seq 1 15); do
  if echo "get /gcs/copies/upload.bin $work/copied.bin" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null 2>&1; then
    copied=yes
    break
  fi
  sleep 1
done
[ -n "$copied" ] || fail "the on_upload copy never appeared in /gcs/copies"
cmp -s "$work/upload.bin" "$work/copied.bin" || fail "the copied file differs from the uploaded file"

echo "checking the scheduled pull transfer..."
echo "pulled by a transfer" > "$work/statement.pull"
echo "put $work/statement.pull /s3/statement.pull" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null
pulled=""
for _ in $(seq 1 20); do
  if echo "get /gcs/pulled/statement.pull $work/pulled.txt" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null 2>&1; then
    pulled=yes
    break
  fi
  sleep 1
done
[ -n "$pulled" ] || fail "the transfer never pulled statement.pull into /gcs/pulled"
cmp -s "$work/statement.pull" "$work/pulled.txt" || fail "the pulled file differs"
for _ in $(seq 1 5); do
  echo "ls /s3/statement.pull" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null 2>&1 || break
  sleep 1
done
if echo "ls /s3/statement.pull" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null 2>&1; then
  fail "delete_after did not remove the pulled file from /s3"
fi

echo "checking that shell access is refused..."
if ssh -q -i "$KEY" -p "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes "demo@$HOST" true 2>/dev/null; then
  fail "a shell command was allowed"
fi

echo "checking metrics..."
curl -fs "$HTTP/metrics" | grep -q 'sftpgate_operations_total{account="demo",folder="/s3",op="upload",result="ok"}' \
  || fail "upload metric missing"

echo "checking the read-only API and UI..."
curl -fs -u "e2e:$UI_TOKEN" "$HTTP/api/v1/status" | grep -q '"ready": true' || fail "API status is not ready"
curl -fs -u "e2e:$UI_TOKEN" "$HTTP/api/v1/events?event=upload&account=demo" | grep -q '"result": "ok"' || fail "API shows no upload events"
curl -fs -u "e2e:$UI_TOKEN" "$HTTP/ui/" | grep -q 'Open sessions' || fail "UI overview does not load"
status=$(curl -s -o /dev/null -w '%{http_code}' "$HTTP/ui/")
[ "$status" = "401" ] || fail "UI without a token answered $status, want 401"
status=$(curl -s -o /dev/null -w '%{http_code}' -X POST -u "e2e:$UI_TOKEN" "$HTTP/api/v1/status")
[ "$status" = "405" ] || fail "POST to the API answered $status, want 405"

echo "checking that Prometheus scrapes sftpgate..."
for _ in $(seq 1 30); do
  curl -fs "$PROMETHEUS/api/v1/targets" | grep -q '"health":"up"' && break
  sleep 2
done
curl -fs "$PROMETHEUS/api/v1/targets" | grep -q '"health":"up"' || fail "Prometheus target is not up"
curl -fs "$PROMETHEUS/api/v1/rules" | grep -q 'SftpgateStorageErrors' || fail "alert rules are not loaded"

echo "checking the Grafana dashboard..."
curl -fs "$GRAFANA/api/dashboards/uid/sftpgate" | grep -q '"title":"SFTPgate"' || fail "Grafana dashboard is missing"

echo "checking that traces reach Jaeger..."
traced=""
for _ in $(seq 1 20); do
  if curl -fs "$JAEGER/api/traces?service=sftpgate&operation=sftp.upload&limit=1" | grep -q '"operationName":"sftp.upload"'; then
    traced=yes
    break
  fi
  sleep 1
done
[ -n "$traced" ] || fail "no sftp.upload trace in Jaeger"

echo "all end-to-end checks passed"
