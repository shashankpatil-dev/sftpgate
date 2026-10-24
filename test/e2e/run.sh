#!/usr/bin/env bash
# End-to-end checks against the docker compose setup, using the real
# OpenSSH sftp client. Run `make up` first.
set -euo pipefail

HOST=${HOST:-localhost}
PORT=${PORT:-2222}
HTTP=${HTTP:-http://localhost:8080}
PROMETHEUS=${PROMETHEUS:-http://localhost:9090}
GRAFANA=${GRAFANA:-http://localhost:3000}
KEY=${KEY:-.dev/keys/demo_user}

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

echo "checking that shell access is refused..."
if ssh -q -i "$KEY" -p "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes "demo@$HOST" true 2>/dev/null; then
  fail "a shell command was allowed"
fi

echo "checking metrics..."
curl -fs "$HTTP/metrics" | grep -q 'sftpgate_operations_total{account="demo",folder="/s3",op="upload",result="ok"}' \
  || fail "upload metric missing"

echo "checking that Prometheus scrapes sftpgate..."
for _ in $(seq 1 30); do
  curl -fs "$PROMETHEUS/api/v1/targets" | grep -q '"health":"up"' && break
  sleep 2
done
curl -fs "$PROMETHEUS/api/v1/targets" | grep -q '"health":"up"' || fail "Prometheus target is not up"
curl -fs "$PROMETHEUS/api/v1/rules" | grep -q 'SftpgateStorageErrors' || fail "alert rules are not loaded"

echo "checking the Grafana dashboard..."
curl -fs "$GRAFANA/api/dashboards/uid/sftpgate" | grep -q '"title":"SFTPgate"' || fail "Grafana dashboard is missing"

echo "all end-to-end checks passed"
