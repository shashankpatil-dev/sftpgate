#!/usr/bin/env bash
# Installs the Helm chart on a local kind cluster and checks it really works:
# SFTP upload and download, health, the UI, transfers on one pod only,
# hot reload of a config change, the security settings, and git-sync.
#
#   test/k8s/smoke.sh            # needs docker, kind, kubectl, helm
#   KEEP=1 test/k8s/smoke.sh     # leave the cluster running afterwards
set -euo pipefail
cd "$(dirname "$0")/../.."

CLUSTER=${CLUSTER:-sftpgate-test}
NS=sftpgate
CHART=deploy/helm/sftpgate
# Local ports for port-forward, away from the Docker Compose setup's 2222/8080.
SFTP_PORT=12222
HTTP_PORT=18080
work=$(mktemp -d)
pids=()

cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  rm -rf "$work"
  if [ -z "${KEEP:-}" ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  kubectl -n "$NS" get pods -o wide >&2 || true
  kubectl -n "$NS" logs statefulset/sftpgate --all-containers --tail=30 >&2 || true
  exit 1
}

# forward starts a port-forward in the background and waits until it answers.
# It fails if the port-forward itself dies, for example when the local port
# is already taken, instead of quietly talking to whatever else listens there.
forward() {
  local port=${2%%:*}
  nc -z localhost "$port" 2>/dev/null && fail "local port $port is already in use"
  kubectl -n "$NS" port-forward "$1" "$2" >"$work/forward-$port.log" 2>&1 &
  local pid=$!
  pids+=("$pid")
  for _ in $(seq 1 30); do
    kill -0 "$pid" 2>/dev/null || fail "port-forward to $1 stopped: $(cat "$work/forward-$port.log")"
    nc -z localhost "$port" 2>/dev/null && return
    sleep 1
  done
  fail "port-forward to $1 did not start"
}

stop_forwards() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  pids=()
  sleep 1
}

sftp_run() {
  sftp -q -i "$work/demo_user" -P "$SFTP_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes -b - demo@localhost
}

echo "creating the kind cluster..."
kind get clusters | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --wait 120s

echo "building and loading the image..."
docker build -q -t sftpgate:test . >/dev/null
kind load docker-image sftpgate:test --name "$CLUSTER" >/dev/null

echo "creating the secrets..."
# Start from a clean install, so a rerun with KEEP=1 tests a fresh deployment.
helm uninstall sftpgate -n "$NS" --wait >/dev/null 2>&1 || true
kubectl -n "$NS" delete pvc --all --wait >/dev/null 2>&1 || true
kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
ssh-keygen -q -t ed25519 -N "" -f "$work/ssh_host_ed25519_key"
ssh-keygen -q -t ed25519 -N "" -f "$work/demo_user"
kubectl -n "$NS" create secret generic sftpgate-host-keys --from-file="$work/ssh_host_ed25519_key" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "$NS" create secret generic sftpgate-test-demo-key --from-file="$work/demo_user.pub" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "$NS" create secret generic sftpgate-test-env --from-literal=UI_TOKEN=kind-smoke-test-token \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

echo "installing the chart..."
helm upgrade --install sftpgate "$CHART" -n "$NS" -f test/k8s/values.yaml --wait --timeout 180s >/dev/null \
  || fail "helm install did not become ready"

echo "checking SFTP through the service..."
forward service/sftpgate "$SFTP_PORT:22"
head -c 3000000 /dev/urandom > "$work/upload.bin"
printf 'put %s /files/upload.bin\nget /files/upload.bin %s\n' "$work/upload.bin" "$work/download.bin" | sftp_run >/dev/null \
  || fail "sftp upload/download failed"
cmp -s "$work/upload.bin" "$work/download.bin" || fail "downloaded file differs"

echo "checking health, readiness and the UI..."
forward service/sftpgate-http "$HTTP_PORT:8080"
curl -fs localhost:$HTTP_PORT/healthz >/dev/null || fail "/healthz"
curl -fs localhost:$HTTP_PORT/readyz >/dev/null || fail "/readyz"
curl -fs -u x:kind-smoke-test-token localhost:$HTTP_PORT/api/v1/status | grep -q '"ready": true' || fail "API status"
[ "$(curl -s -o /dev/null -w '%{http_code}' localhost:$HTTP_PORT/ui/)" = 401 ] || fail "UI must ask for a token"

echo "checking that transfers run on the first pod only..."
kubectl -n "$NS" logs sftpgate-1 -c sftpgate | grep -q "scheduled transfers run on another replica" \
  || fail "sftpgate-1 should leave transfers to sftpgate-0"
if kubectl -n "$NS" logs sftpgate-0 -c sftpgate | grep -q "run on another replica"; then
  fail "sftpgate-0 should run the transfers"
fi

echo "checking the security settings..."
sec=$(kubectl -n "$NS" get pod sftpgate-0 -o jsonpath='{.spec.containers[0].securityContext.readOnlyRootFilesystem} {.spec.containers[0].securityContext.runAsNonRoot} {.spec.automountServiceAccountToken}')
[ "$sec" = "true true false" ] || fail "unexpected security settings: $sec"

echo "checking hot reload of a config change (can take a minute)..."
restarts_before=$(kubectl -n "$NS" get pod sftpgate-0 -o jsonpath='{.status.containerStatuses[0].restartCount}')
sed 's/^    reload_interval: 2s$/&\n    banner: "changed by helm upgrade"/' test/k8s/values.yaml > "$work/changed.yaml"
grep -q 'changed by helm upgrade' "$work/changed.yaml" || fail "test setup: could not change the config"
helm upgrade sftpgate "$CHART" -n "$NS" -f "$work/changed.yaml" >/dev/null
reloaded=""
for _ in $(seq 1 120); do
  if kubectl -n "$NS" logs sftpgate-0 -c sftpgate | grep -q '"msg":"config reloaded"'; then reloaded=yes; break; fi
  sleep 1
done
[ -n "$reloaded" ] || fail "the config change was not picked up"
[ "$(kubectl -n "$NS" get pod sftpgate-0 -o jsonpath='{.status.containerStatuses[0].restartCount}')" = "$restarts_before" ] \
  || fail "the pod restarted instead of reloading"

if [ -n "${GITSYNC_REF:-}" ]; then
  echo "checking git-sync mode (config from GitHub at $GITSYNC_REF)..."
  helm upgrade sftpgate "$CHART" -n "$NS" -f test/k8s/values.yaml --set replicas=1 \
    --set gitSync.enabled=true \
    --set gitSync.repo=https://github.com/shashankpatil-dev/sftpgate.git \
    --set gitSync.ref="$GITSYNC_REF" \
    --set gitSync.path=test/k8s/gitops-config.yaml \
    --wait --timeout 180s >/dev/null || fail "git-sync install did not become ready"
  stop_forwards
  forward service/sftpgate "$SFTP_PORT:22"
  echo "ls /" | sftp_run 2>/dev/null | grep -q "from-git" || fail "the config from git is not in use"
  forward service/sftpgate-http "$HTTP_PORT:8080"
  curl -fs localhost:$HTTP_PORT/metrics | grep 'sftpgate_config_info' | grep -q "revision=\"[0-9a-f]\{40\}\"" \
    || fail "the live git commit is not reported"
fi

echo "all Kubernetes checks passed"
