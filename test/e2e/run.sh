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
API_TOKEN=${API_TOKEN:-dev-only-api-token}
ADMIN_TOKEN=${ADMIN_TOKEN:-dev-only-admin-token}

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
[ -n "$copied" ] || fail "the after_upload copy never appeared in /gcs/copies"
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

# Every run uses new names, so earlier runs never get in the way.
run_id=$(date +%s%N)
GNUPG=${GNUPG:-.dev/keys/gnupg}
HOOKS=${HOOKS:-http://localhost:9999}
gpg_do() {
  gpg --homedir "$GNUPG" --batch --yes --quiet --pinentry-mode loopback \
    --passphrase dev-only-pgp-passphrase --trust-model always "$@"
}
# wait_get NAME LOCAL: download NAME as soon as it exists.
wait_get() {
  for _ in $(seq 1 20); do
    echo "get $1 $2" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}
exists() {
  echo "ls $1" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null 2>&1
}

echo "checking before_upload: a good CSV is accepted, a bad one refused with the reason..."
printf 'id,amount\n1,100\n' > "$work/good.csv"
printf 'name,total\nx,1\n' > "$work/bad.csv"
echo "put $work/good.csv /recon/good-$run_id.csv" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null \
  || fail "a good CSV was refused by /recon"
if echo "put $work/bad.csv /recon/bad-$run_id.csv" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null 2>&1; then
  fail "a bad CSV was accepted by /recon"
fi
if exists "/recon/bad-$run_id.csv"; then fail "a refused file was stored"; fi
# The reason goes to the client in the SFTP status message. OpenSSH's sftp
# only prints "Failure", so check what was recorded instead.
grep -q 'CSV header is' <<<"$(curl -fs -H "Authorization: Bearer $API_TOKEN" "$HTTP/api/v1/events?event=upload&account=demo&result=denied")" \
  || fail "the refusal and its reason are not in the audit log"

echo "checking after_upload with pgp_decrypt (signed), check and copy..."
gpg_do --sign --encrypt -r demo@sftpgate.local -o "$work/report.csv.pgp" "$work/good.csv"
echo "put $work/report.csv.pgp /pgp/report-$run_id.csv.pgp" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null
today=$(TZ=Asia/Kolkata date +%F)
wait_get "/gcs/decrypted/$today/report-$run_id.csv" "$work/decrypted.csv" \
  || fail "the decrypted copy never appeared in /gcs/decrypted/$today"
cmp -s "$work/good.csv" "$work/decrypted.csv" || fail "the decrypted copy differs from the original"

echo "checking before_push (check + pgp_encrypt) and after: move..."
printf 'id\n' > "$work/push-bad.csv"
printf -- '-mkdir /s3/outbox\nput %s /s3/outbox/push-%s.csv\nput %s /s3/outbox/push-bad-%s.csv\n' \
  "$work/good.csv" "$run_id" "$work/push-bad.csv" "$run_id" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null
wait_get "/gcs/inbox/push-$run_id.csv.pgp" "$work/pushed.pgp" \
  || fail "the pushed file never arrived as /gcs/inbox/push-$run_id.csv.pgp"
gpg_do -o "$work/pushed.csv" --decrypt "$work/pushed.pgp"
cmp -s "$work/good.csv" "$work/pushed.csv" || fail "the pushed file doesn't decrypt to the original"
if exists "/gcs/inbox/push-$run_id.csv"; then fail "the plain file must never be sent"; fi
exists "/s3/sent/push-$run_id.csv" || fail "a sent file must move to /s3/sent"
exists "/s3/outbox/push-bad-$run_id.csv" || fail "a file that fails its check must stay in the outbox"
if exists "/gcs/inbox/push-bad-$run_id.csv.pgp"; then fail "a file that fails its check must not be sent"; fi
# Remove it, or every push run would fail on it again.
echo "rm /s3/outbox/push-bad-$run_id.csv" | sftp "${ssh_opts[@]}" -b - "demo@$HOST" >/dev/null

echo "checking the webhooks of every hook (signed, with the custom header)..."
check_hooks() {
  curl -fs "$HOOKS/calls" | python3 -c '
import json, sys
run_id, pull_name = sys.argv[1], sys.argv[2]
calls = json.load(sys.stdin)
def find(event, name):
    return [c for c in calls if c["payload"]["event"] == event and name in c["payload"]["filename"]]
want = {
    "upload to /recon": find("upload", "good-" + run_id),
    "upload to /pgp": find("upload", "report-" + run_id),
    "download from /s3": find("download", "upload.bin"),
    "push": find("push", "push-" + run_id + ".csv"),
    "pull": find("pull", pull_name),
}
missing = [k for k, v in want.items() if not v]
if missing:
    sys.exit("missing webhooks: " + ", ".join(missing))
bad = [c for c in calls if not c["signature_ok"] or c["x_team"] != "demo"]
if bad:
    sys.exit("%d webhooks with a bad signature or without the X-Team header" % len(bad))
push = want["push"][0]["payload"]
if push["transfer"] != "demo-push" or not push["remote_path"].endswith(".pgp") or push["key"] != "demo/sent/push-%s.csv" % run_id:
    sys.exit("push webhook payload: %r" % push)
pgp = want["upload to /pgp"][0]["payload"]
if not pgp["copies"] or "/decrypted/" not in pgp["copies"][0]["key"]:
    sys.exit("the /pgp webhook has no decrypted copy: %r" % pgp)
' "$run_id" "$pull_name"
}
for _ in $(seq 1 20); do
  check_hooks 2>/dev/null && break
  sleep 1
done
check_hooks || fail "webhook checks failed"

echo "checking that shell access is refused..."
if ssh -q -i "$KEY" -p "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes "demo@$HOST" true 2>/dev/null; then
  fail "a shell command was allowed"
fi

echo "checking metrics..."
grep -q 'sftpgate_operations_total{account="demo",folder="/s3",op="upload",result="ok"}' <<<"$(curl -fs "$HTTP/metrics")" \
  || fail "upload metric missing"

echo "checking the read-only API..."
api=(-H "Authorization: Bearer $API_TOKEN")
admin=(-H "Authorization: Bearer $ADMIN_TOKEN")
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
grep -q '"ready": true' <<<"$(curl -fs "${api[@]}" "$HTTP/api/v1/status")" || fail "API status is not ready"
grep -q '"result": "ok"' <<<"$(curl -fs "${api[@]}" "$HTTP/api/v1/events?event=upload&account=demo")" || fail "API shows no upload events"
[ "$(code "$HTTP/api/v1/status")" = 401 ] || fail "API without a token must answer 401"
[ "$(code -X POST "${api[@]}" "$HTTP/api/v1/status")" = 405 ] || fail "POST to a read-only call must answer 405"
[ "$(code "${api[@]}" "$HTTP/ui/")" = 404 ] || fail "the HTML UI is removed, /ui/ must answer 404"

echo "checking the admin calls..."
[ "$(code -X POST "${api[@]}" "$HTTP/api/v1/transfers/demo-push/run")" = 401 ] || fail "a read-only token must not run a transfer"
[ "$(code -X POST -u "e2e:$ADMIN_TOKEN" "$HTTP/api/v1/transfers/demo-push/run")" = 401 ] || fail "admin calls take only a Bearer token"
[ "$(code -X POST "${admin[@]}" "$HTTP/api/v1/transfers/no-such/run")" = 404 ] || fail "an unknown transfer must answer 404"
# demo-push runs every 5 seconds, so it may be busy right now: 202 or 409.
status=$(code -X POST "${admin[@]}" "$HTTP/api/v1/transfers/demo-push/run")
[ "$status" = 202 ] || [ "$status" = 409 ] || fail "run transfer now answered $status, want 202 or 409"
grep -q '"retried"' <<<"$(curl -fs -X POST "${admin[@]}" "$HTTP/api/v1/actions/retry")" || fail "retry actions failed"
[ "$(code -X POST "${admin[@]}" "$HTTP/api/v1/actions/retry?job=no-such")" = 404 ] || fail "retrying an unknown job must answer 404"
grep -q '"method": "run_transfer"' <<<"$(curl -fs "${api[@]}" "$HTTP/api/v1/events?event=admin")" || fail "admin calls are not audited"

echo "checking that Prometheus scrapes sftpgate..."
for _ in $(seq 1 30); do
  grep -q '"health":"up"' <<<"$(curl -fs "$PROMETHEUS/api/v1/targets")" && break
  sleep 2
done
grep -q '"health":"up"' <<<"$(curl -fs "$PROMETHEUS/api/v1/targets")" || fail "Prometheus target is not up"
grep -q 'SftpgateStorageErrors' <<<"$(curl -fs "$PROMETHEUS/api/v1/rules")" || fail "alert rules are not loaded"

echo "checking the Grafana dashboard..."
grep -q '"title":"SFTPgate"' <<<"$(curl -fs "$GRAFANA/api/dashboards/uid/sftpgate")" || fail "Grafana dashboard is missing"

echo "checking that traces reach Jaeger (sessions, logins, transfers, actions)..."
for op in sftp.upload sftp.login storage.read transfer.run transfer.file action.run; do
  traced=""
  for _ in $(seq 1 20); do
    if grep -q "\"operationName\":\"$op\"" <<<"$(curl -fs "$JAEGER/api/traces?service=sftpgate&operation=$op&limit=1")"; then
      traced=yes
      break
    fi
    sleep 1
  done
  [ -n "$traced" ] || fail "no $op span in Jaeger"
done
grep -q '"key":"sftpgate.result"' <<<"$(curl -fs "$JAEGER/api/traces?service=sftpgate&operation=transfer.run&limit=1")" \
  || fail "transfer.run spans must carry sftpgate.result"

echo "all end-to-end checks passed"
