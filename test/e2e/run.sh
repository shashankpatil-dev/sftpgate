#!/usr/bin/env bash
# End-to-end checks against the docker compose setup, using the real
# OpenSSH sftp client. Run `make up` first.
set -euo pipefail
# Responses are read in full before grep looks at them: "curl | grep -q" can
# fail under pipefail when grep stops reading early.

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
# A new name each run: a file already pulled before is skipped, not deleted.
pull_name="statement-$(date +%s%N).pull"
echo "pulled by a transfer" > "$work/statement.pull"
echo "put $work/statement.pull /s3/$pull_name" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null
pulled=""
for _ in $(seq 1 20); do
  if echo "get /gcs/pulled/$pull_name $work/pulled.txt" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null 2>&1; then
    pulled=yes
    break
  fi
  sleep 1
done
[ -n "$pulled" ] || fail "the transfer never pulled $pull_name into /gcs/pulled"
cmp -s "$work/statement.pull" "$work/pulled.txt" || fail "the pulled file differs"
for _ in $(seq 1 5); do
  echo "ls /s3/$pull_name" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null 2>&1 || break
  sleep 1
done
if echo "ls /s3/$pull_name" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null 2>&1; then
  fail "delete_after did not remove the pulled file from /s3"
fi

echo "checking that shell access is refused..."
if ssh -q -i "$KEY" -p "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes "demo@$HOST" true 2>/dev/null; then
  fail "a shell command was allowed"
fi

echo "checking metrics..."
grep -q 'sftpgate_operations_total{account="demo",folder="/s3",op="upload",result="ok"}' <<<"$(curl -fs "$HTTP/metrics")" \
  || fail "upload metric missing"

echo "checking the read-only API and UI..."
grep -q '"ready": true' <<<"$(curl -fs -u "e2e:$UI_TOKEN" "$HTTP/api/v1/status")" || fail "API status is not ready"
grep -q '"result": "ok"' <<<"$(curl -fs -u "e2e:$UI_TOKEN" "$HTTP/api/v1/events?event=upload&account=demo")" || fail "API shows no upload events"
grep -q 'Open sessions' <<<"$(curl -fs -u "e2e:$UI_TOKEN" "$HTTP/ui/")" || fail "UI overview does not load"
status=$(curl -s -o /dev/null -w '%{http_code}' "$HTTP/ui/")
[ "$status" = "401" ] || fail "UI without a token answered $status, want 401"
status=$(curl -s -o /dev/null -w '%{http_code}' -X POST -u "e2e:$UI_TOKEN" "$HTTP/api/v1/status")
[ "$status" = "405" ] || fail "POST to the API answered $status, want 405"

echo "checking that Prometheus scrapes sftpgate..."
for _ in $(seq 1 30); do
  grep -q '"health":"up"' <<<"$(curl -fs "$PROMETHEUS/api/v1/targets")" && break
  sleep 2
done
grep -q '"health":"up"' <<<"$(curl -fs "$PROMETHEUS/api/v1/targets")" || fail "Prometheus target is not up"
grep -q 'SftpgateStorageErrors' <<<"$(curl -fs "$PROMETHEUS/api/v1/rules")" || fail "alert rules are not loaded"

echo "checking the Grafana dashboard..."
grep -q '"title":"SFTPgate"' <<<"$(curl -fs "$GRAFANA/api/dashboards/uid/sftpgate")" || fail "Grafana dashboard is missing"

echo "checking that traces reach Jaeger..."
traced=""
for _ in $(seq 1 20); do
  if grep -q '"operationName":"sftp.upload"' <<<"$(curl -fs "$JAEGER/api/traces?service=sftpgate&operation=sftp.upload&limit=1")"; then
    traced=yes
    break
  fi
  sleep 1
done
[ -n "$traced" ] || fail "no sftp.upload trace in Jaeger"

echo "all end-to-end checks passed"
