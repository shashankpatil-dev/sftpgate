#!/usr/bin/env bash
# Load test of SFTPgate on a local kind cluster.
#
# It installs SFTPgate with the Helm chart, an S3 mock and Prometheus, with
# fixed CPU and memory limits, then runs each scenario as a Kubernetes Job
# inside the cluster. For every scenario it saves the load generator's report
# (client-side latencies, errors, integrity) and Prometheus numbers
# (server-side p95, CPU, memory, goroutines, sessions), then writes a summary.
#
#   test/load/run.sh                 # daily burst big limit chaos-pods chaos-storage
#   SCENARIOS="daily soak" SOAK=1h test/load/run.sh
#   KEEP=1 test/load/run.sh          # keep the cluster afterwards
#
# Needs docker, kind, kubectl and helm (from PATH or .dev/bin).
set -euo pipefail
cd "$(dirname "$0")/../.."
export PATH="$PWD/.dev/bin:$PATH"

CLUSTER=sftpgate-load
NS=sftpgate-load
PROM_PORT=19090
GRAFANA_PORT=13000
JAEGER_PORT=16686
OUT=${OUT:-test/load/results/$(date +%Y%m%d-%H%M%S)}
SCENARIOS=${SCENARIOS:-"daily burst big limit chaos-pods chaos-storage"}
work=$(mktemp -d)
pids=()

cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  rm -rf "$work"
  if [ -z "${KEEP:-}" ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

log() { echo "[$(date +%H:%M:%S)] $*"; }
k() { kubectl -n "$NS" "$@"; }

setup() {
  log "creating the cluster"
  grep -qx "$CLUSTER" <<<"$(kind get clusters 2>/dev/null)" || kind create cluster --name "$CLUSTER" --wait 120s >/dev/null

  log "building and loading images"
  docker build -q -t sftpgate:test . >/dev/null
  mkdir -p "$work/loadgen-image"
  CGO_ENABLED=0 go build -trimpath -o "$work/loadgen-image/loadgen" ./test/load/loadgen
  docker build -q -f test/load/Dockerfile -t sftpgate-loadgen:test "$work/loadgen-image" >/dev/null
  kind load docker-image sftpgate:test sftpgate-loadgen:test --name "$CLUSTER" >/dev/null

  log "creating secrets"
  kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  ssh-keygen -q -t ed25519 -N "" -f "$work/ssh_host_ed25519_key"
  ssh-keygen -q -t ed25519 -N "" -f "$work/user"
  k create secret generic sftpgate-host-keys --from-file="$work/ssh_host_ed25519_key" --dry-run=client -o yaml | k apply -f - >/dev/null
  k create secret generic load-user-key --from-file=user.pub="$work/user.pub" --dry-run=client -o yaml | k apply -f - >/dev/null
  k create secret generic loadgen-key --from-file=id="$work/user" --dry-run=client -o yaml | k apply -f - >/dev/null
  k create secret generic load-env --from-literal=AWS_ACCESS_KEY_ID=test --from-literal=AWS_SECRET_ACCESS_KEY=test \
    --dry-run=client -o yaml | k apply -f - >/dev/null

  log "starting the S3 storage, Prometheus, Grafana and Jaeger"
  k create configmap grafana-dashboard --from-file=sftpgate.json=deploy/helm/sftpgate/files/dashboard.json \
    --dry-run=client -o yaml | k apply -f - >/dev/null
  k apply -f test/load/k8s/s3.yaml -f test/load/k8s/prometheus.yaml -f test/load/k8s/observability.yaml >/dev/null
  k rollout status deploy/grafana --timeout=180s >/dev/null
  k rollout status deploy/jaeger --timeout=180s >/dev/null
  k rollout status deploy/s3 --timeout=180s >/dev/null
  k rollout status deploy/prometheus --timeout=180s >/dev/null
  make_bucket

  log "installing SFTPgate"
  helm upgrade --install sftpgate deploy/helm/sftpgate -n "$NS" -f test/load/values.yaml --set replicas=1 --wait --timeout 180s >/dev/null

  for fwd in "prometheus $PROM_PORT:9090" "grafana $GRAFANA_PORT:3000" "jaeger $JAEGER_PORT:16686"; do
    # shellcheck disable=SC2086
    k port-forward svc/${fwd% *} ${fwd#* } >/dev/null 2>&1 &
    pids+=($!)
  done
  sleep 2
  mkdir -p "$OUT"
  log "watch live: Grafana http://localhost:$GRAFANA_PORT  Jaeger http://localhost:$JAEGER_PORT  Prometheus http://localhost:$PROM_PORT"
}

# make_bucket creates the "load" bucket (S3 CreateBucket is a PUT on /load)
# and checks that it really exists: right after a restart the storage can
# answer the PUT without creating anything.
make_bucket() {
  k delete pod make-bucket --ignore-not-found --wait >/dev/null
  k run make-bucket --restart=Never --image=curlimages/curl:8.11.1 --command -- \
    sh -c 'for i in $(seq 1 90); do curl -fs -X PUT http://s3:8333/load; curl -fs -o /dev/null -I http://s3:8333/load && exit 0; sleep 2; done; exit 1' >/dev/null
  k wait --for=jsonpath='{.status.phase}'=Succeeded pod/make-bucket --timeout=180s >/dev/null
  k delete pod make-bucket --wait=false >/dev/null
}

# deploy sets the replica count (and optionally a changed values file) and
# waits until every pod is ready.
deploy() {
  local replicas=$1 values=${2:-test/load/values.yaml}
  helm upgrade sftpgate deploy/helm/sftpgate -n "$NS" -f "$values" --set replicas="$replicas" --wait --timeout 300s >/dev/null
  k rollout status statefulset/sftpgate --timeout=300s >/dev/null
  # Let Prometheus find the pods before the scenario starts.
  sleep 10
}

# scenario NAME REPLICAS CHAOS LOADGEN_ARGS...
scenario() {
  local name=$1 replicas=$2 chaos=$3
  shift 3
  log "scenario $name: $replicas pod(s), loadgen $*"

  local start
  start=$(date +%s)
  k delete job loadgen --ignore-not-found --wait >/dev/null
  cat <<EOF | k apply -f - >/dev/null
apiVersion: batch/v1
kind: Job
metadata:
  name: loadgen
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: loadgen
          image: sftpgate-loadgen:test
          imagePullPolicy: Never
          args: [-addr, "sftpgate:22", -user, loadtest, -key, /keys/id, -scenario, "$name", $(printf '"%s", ' "$@" | sed 's/, $//')]
          resources:
            requests: { cpu: "2", memory: 512Mi }
          volumeMounts:
            - { name: key, mountPath: /keys }
      volumes:
        - name: key
          secret: { secretName: loadgen-key, defaultMode: 0444 }
EOF

  if [ "$chaos" != none ]; then
    "$chaos" &
    pids+=($!)
  fi
  k wait --for=condition=complete job/loadgen --timeout=900s >/dev/null || {
    k logs job/loadgen | tail -20
    echo "loadgen job did not complete" >&2
    exit 1
  }
  local end
  end=$(date +%s)

  grep '^LOADGEN-REPORT ' <<<"$(k logs job/loadgen)" | sed 's/^LOADGEN-REPORT //' > "$OUT/$name.client.json"
  server_numbers "$name" $((end - start)) > "$OUT/$name.server.json"
  k get pods -l app.kubernetes.io/name=sftpgate -o jsonpath='{range .items[*]}{.metadata.name}={.status.containerStatuses[0].restartCount} {end}' > "$OUT/$name.restarts.txt"
  log "scenario $name done in $((end - start))s"
}

# server_numbers asks Prometheus about the scenario's time window.
server_numbers() {
  local name=$1 secs=$2
  local w="${secs}s"
  python3 - "$PROM_PORT" "$w" "$name" <<'PY'
import json, sys, urllib.parse, urllib.request
port, w, name = sys.argv[1], sys.argv[2], sys.argv[3]
queries = {
  "server_upload_p95_ms":   f'histogram_quantile(0.95, sum by (le) (increase(sftpgate_transfer_duration_seconds_bucket{{direction="in"}}[{w}]))) * 1000',
  "server_download_p95_ms": f'histogram_quantile(0.95, sum by (le) (increase(sftpgate_transfer_duration_seconds_bucket{{direction="out"}}[{w}]))) * 1000',
  "cpu_cores_max_per_pod":  f'max(max_over_time(rate(process_cpu_seconds_total[15s])[{w}:5s]))',
  "cpu_cores_avg_total":    f'avg_over_time(sum(rate(process_cpu_seconds_total[15s]))[{w}:5s])',
  "memory_mb_max_per_pod":  f'max(max_over_time(process_resident_memory_bytes[{w}])) / 1e6',
  "goroutines_max_per_pod": f'max(max_over_time(go_goroutines[{w}]))',
  "sessions_peak":          f'max_over_time(sum(sftpgate_sessions_active)[{w}:5s])',
  "uploads_ok":             f'sum(increase(sftpgate_operations_total{{op="upload",result="ok"}}[{w}]))',
  "uploads_failed":         f'sum(increase(sftpgate_operations_total{{op="upload",result="failed"}}[{w}]))',
  "storage_errors":         f'sum(increase(sftpgate_storage_errors_total[{w}]))',
  "connections_rejected":   f'sum(increase(sftpgate_connections_total{{result=~"rejected.*"}}[{w}]))',
  "spool_free_min_gb":      f'min(min_over_time(sftpgate_spool_free_bytes[{w}])) / 1e9',
}
out = {"scenario": name}
for key, q in queries.items():
    url = f"http://localhost:{port}/api/v1/query?" + urllib.parse.urlencode({"query": q})
    try:
        res = json.load(urllib.request.urlopen(url, timeout=20))["data"]["result"]
        val = float(res[0]["value"][1]) if res else None
        out[key] = None if val is None or val != val else round(val, 2)
    except Exception as e:
        out[key] = f"error: {e}"
print(json.dumps(out, indent=2))
PY
}

# Chaos: delete two SFTPgate pods while the load runs.
kill_pods() {
  sleep 40
  log "chaos: deleting pod sftpgate-1"
  k delete pod sftpgate-1 --wait=false >/dev/null
  sleep 30
  log "chaos: deleting pod sftpgate-2"
  k delete pod sftpgate-2 --wait=false >/dev/null
}

# Chaos: take the storage away for 20 seconds.
storage_outage() {
  sleep 30
  log "chaos: storage down"
  k scale deploy/s3 --replicas=0 >/dev/null
  sleep 20
  log "chaos: storage back"
  k scale deploy/s3 --replicas=1 >/dev/null
  k rollout status deploy/s3 --timeout=120s >/dev/null
  # The storage keeps its data in memory-backed scratch space, so the
  # bucket has to be made again after the restart.
  make_bucket
}

setup

for s in $SCENARIOS; do
  case $s in
    daily)
      # A normal day: a handful of partners sending medium files.
      deploy 1
      scenario daily 1 none -folder /large -sessions 10 -duration 180s -ramp 10s -size 5MiB ;;
    burst)
      # End-of-day rush: as many sessions as max_connections allows.
      deploy 1
      scenario burst 1 none -folder /small -sessions 25 -duration 120s -ramp 5s -size 256KiB ;;
    big)
      # Big files: 5 partners each sending 500 MiB at the same time.
      deploy 1
      scenario big 1 none -folder /large -sessions 5 -files 2 -ramp 2s -size 500MiB ;;
    limit)
      # More sessions than max_connections: the extra ones are refused cleanly.
      deploy 1
      scenario limit 1 none -folder /small -sessions 35 -duration 60s -ramp 5s -size 256KiB ;;
    chaos-pods)
      # Pods deleted in the middle of 200 MiB transfers.
      deploy 3
      scenario chaos-pods 3 kill_pods -folder /large -sessions 10 -duration 120s -ramp 5s -size 200MiB ;;
    chaos-storage)
      # The storage disappears for 20 seconds during uploads.
      deploy 2
      scenario chaos-storage 2 storage_outage -folder /small -sessions 10 -duration 100s -ramp 5s -size 1MiB ;;
    soak)
      # A long, steady run to catch slow leaks (memory, goroutines, disk).
      deploy 1
      scenario soak 1 none -folder /large -sessions 10 -duration "${SOAK:-30m}" -ramp 10s -size 2MiB ;;
    *) echo "unknown scenario $s" >&2; exit 1 ;;
  esac
done

python3 test/load/summary.py "$OUT" > "$OUT/summary.md"
log "results in $OUT"
cat "$OUT/summary.md"
