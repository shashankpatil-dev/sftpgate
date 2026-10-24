# Load test

How SFTPgate behaves under realistic load and failures, measured on a
Kubernetes cluster, and what the tests found and fixed.

Run it yourself: `test/load/run.sh` (see [How to run it](#how-to-run-it)).

---

## Summary

- **Correctness held everywhere.** About 30,000 files went up and came back
  down across all scenarios, including pod deletions and a storage outage.
  **No corrupted file and no half file reached storage or a partner.**
- **One small pod (1 CPU, 512 MiB) handles normal SFTP work easily.**
  - 10 partners sending 5 MiB files: 0 errors, upload p95 0.53 s.
  - 25 at once (the connection limit): 0 errors, p95 0.14 s.
  - 500 MiB files: 0 errors, memory under 190 MB.
- **Failures are handled cleanly.**
  - Deleted pods: only those pods' sessions drop; partners reconnect to the others.
  - A storage outage: uploads are refused with a clear "please upload it again", never falsely reported as saved, and the alert fires.
- **The tests found three serious bugs, all fixed before these results:**
  corrupted large downloads, uploads reported as saved when the storage kept
  nothing, and pods running out of memory with large files.

## What was tested

SFTP work in an organization is batch work: a few partners, once or twice a
day, for minutes at a time. The scenarios follow that, with bursts and
failures on top.

| Scenario | What it simulates | Setup |
|---|---|---|
| **daily** | A normal day | 10 sessions, 5 MiB files, 3 minutes, 1 pod |
| **burst** | End-of-day rush | 25 sessions (the `max_connections` default), 256 KiB files, 1 pod |
| **big** | Large files | 5 sessions × 500 MiB at once, 1 pod |
| **limit** | More partners than allowed | 35 sessions against `max_connections: 25`, 1 pod |
| **chaos-pods** | A machine fails mid-transfer | 3 pods, 10 sessions × 200 MiB, two pods deleted |
| **chaos-storage** | The bucket disappears | 2 pods, 10 sessions, storage switched off for 20 s |

Every session logs in like a partner, uploads a file, downloads it back and
**compares its SHA-256 with what it sent**. At the end, every upload that
reported an error is checked again: the file must be missing, or complete and
correct. A half file fails the test.

### Environment

A kind cluster on one laptop (12 CPUs, 14 GB RAM), with fixed limits so
results are repeatable:

| Component | Limits |
|---|---|
| SFTPgate (Helm chart) | **1 CPU, 512 MiB per pod** |
| Storage: SeaweedFS (S3-compatible) | 2 CPU, 2 GiB; wiped before each scenario |
| Load generator (Kubernetes Job in the cluster) | 2 CPU |
| Prometheus, Grafana, Jaeger, 11 alert rules | for watching and measuring |

All parts share one machine, so absolute speeds are lower than on a real
cluster. The comparisons between scenarios, the memory use and the failure
behaviour carry over.

## Results

### What partners experienced (client side)

| Scenario | Files moved | Upload p50 / p95 / p99 | Download p95 | Login p95 | Throughput up | Errors | Corrupted | Half files |
|---|---|---|---|---|---|---|---|---|
| daily | 2,208 | 395 / 532 / 592 ms | 506 ms | 224 ms | 61 MB/s | **0** | 0 | 0 |
| burst | 13,184 | 95 / 140 / 156 ms | 150 ms | 73 ms | 28 MB/s | **0** | 0 | 0 |
| big (500 MiB) | 10 | 31.5 / 36.3 / 36.3 s | 21.1 s | 147 ms | 55 MB/s | **0** | 0 | 0 |
| limit | 6,843 | 95 / 140 / 155 ms | 151 ms | 92 ms | 27 MB/s | 1,192 refused logins (expected) | 0 | 0 |
| chaos-pods | 56 | 11.2 / 27.8 / 35.4 s | 14.0 s | 109 ms | 82 MB/s | 9 (sessions on deleted pods) | 0 | 0 |
| chaos-storage | 2,333 | 71 / 98 / 108 ms | 82 ms | 45 ms | 23 MB/s | uploads during and after the outage (expected) | 0 | 0 |

### What the server used (Prometheus)

| Scenario | Peak sessions | CPU per pod | Memory per pod (peak) | Goroutines (peak) | Pod restarts |
|---|---|---|---|---|---|
| daily | 10 | 1.0 (its limit) | 138 MB | 226 | 0 |
| burst | 25 | 1.0 | 83 MB | 515 | 0 |
| big (500 MiB) | 5 | 1.0 | **183 MB** | 132 | 0 |
| limit | 25 (cap held) | 1.0 | 86 MB | 511 | 0 |
| chaos-pods | 10 | 0.95 | 225 MB | 173 | 0 (deleted pods were replaced) |
| chaos-storage | 10 | 1.0 | 74 MB | 152 | 0 |

### What each scenario showed

- **daily and burst:** zero errors. CPU is the limit: one pod used its full
  1 CPU, mostly on SSH encryption and checksums.
- **big:** five 500 MiB files at once stayed under 190 MB of memory. Memory
  depends on the number of transfers, not on file size.
- **limit:** exactly 25 sessions were served. The other 10 were refused at
  connect (1,254 refusals as clients kept retrying), and the 25 served sessions
  ran as fast as in `burst`. The server stayed healthy.
- **chaos-pods:**
  - Deleting two of three pods dropped only their sessions (9 errors), and those clients reconnected to the remaining pods.
  - 4 uploads finished saving just as their pod died; the client saw an error, but the files were complete and correct.
  - No half files.
- **chaos-storage:**
  - While the storage was down, uploads failed cleanly.
  - After it came back, the test storage briefly said "saved" for uploads into a bucket that didn't exist yet. SFTPgate's read-back caught every one: partners got "the storage did not keep the file, please upload it again", and none were reported as saved.
  - `SftpgateStorageErrors` fired.

### Headroom (earlier runs, before tuning)

Well past normal use, on the same pod sizes:

| Load | Result |
|---|---|
| 200 sessions on 1 pod, 256 KiB files | 0 errors, upload p95 1.4 s (CPU-bound) |
| 200 sessions on 3 pods | 0 errors, upload p95 0.6 s |

## Bugs found and fixed

| # | Problem | Cause | Fix |
|---|---|---|---|
| 1 | **Large downloads came back corrupted** when several ran at once (19 of 20 at 50 MiB) | SFTP clients ask for pieces out of order. The old code kept one long stream from the bucket and dropped it on every jump; under load the storage cut those streams short, and the code treated "cut short" as "end of file". | Files are fetched in fixed 1 MiB windows, each a complete request; the last 3 are kept for out-of-order pieces; a short window is fetched again, or the download fails loudly. Also about 8× faster. |
| 2 | **Uploads reported "saved" when nothing was saved** | An S3-compatible store answered "200 OK" for a bucket that didn't exist | After every save, SFTPgate reads the object's size and checksum back before telling the partner it worked |
| 3 | **Pods killed for memory** with 50 MiB files (about 7 uploads per pod) | The AWS SDK copies each file into memory parts (about 25 MB per file); Go didn't know the container's limit | Smaller send buffers, at most 4 files sent to storage at once per pod, and Go told to stay under 90% of the container limit. 25 × 50 MiB now runs in a 512 MiB pod. |
| 4 | Server-side p95 was meaningless | Histogram buckets too coarse (50 ms, 200 ms, 800 ms...) | Buckets from 10 ms to about 4 minutes |
| 5 | Misleading message after a lost save ("file does not exist") | Error mapping order | Partners now get "please upload it again" |
| 6 | Docs said the bucket verifies MD5 on every save | True for GCS and Azure, not S3 | Docs corrected; the read-back (fix 2) covers every backend |

The test setup also had its own problems (an S3 mock that corrupted itself,
storage filling up with deleted test data). They're fixed in the test tooling,
not in SFTPgate.

## Sizing guidance

| Your situation | Suggested setup |
|---|---|
| A few partners, files up to 100 MB | 1 pod, 1 CPU, 512 MiB (the chart default) |
| Up to 25 partners at once | the same; raise CPU if upload times matter |
| Files up to 500 MB–1 GB | the same memory; disk (`persistence.size`) ≈ largest `max_file_size` × uploads at once |
| A machine failure must not stop partners | 2–3 replicas |
| More than 25 at once | raise `max_connections` and add CPU or replicas |

- **CPU sets the speed;** SSH encryption and checksums use it. **Memory stays flat**, whatever the file size.
- **Keep `max_file_size` close to real file sizes.** Each upload reserves that much disk while it runs, so a 200 MiB limit for 256 KiB files makes uploads queue for space that is never used.

## Alerts

During the test, Prometheus loaded the 11 rules from
`deploy/helm/sftpgate/files/alerts.yml`, and `SftpgateStorageErrors` fired
during the storage outage. Prometheus only **shows** firing alerts; to
**notify** people (Slack, email, PagerDuty), connect it to Alertmanager. On
Kubernetes, `metrics.prometheusRule.enabled: true` gives the rules to your
cluster's Prometheus Operator, which normally already has one.

## How to run it

```sh
test/load/run.sh                              # all scenarios, about 25 minutes
SCENARIOS="daily big" test/load/run.sh        # some of them
SCENARIOS=soak SOAK=1h test/load/run.sh       # long run to look for leaks
KEEP=1 test/load/run.sh                       # keep the cluster afterwards
```

While it runs: Grafana at <http://localhost:13000>, Jaeger at
<http://localhost:16686>, Prometheus and alerts at <http://localhost:19090/alerts>.
Results land in `test/load/results/<run>/` (`summary.md` plus raw JSON).

It needs Docker, kind, kubectl and helm, and about 15 GB of free disk. The
storage is wiped before each scenario.

To find memory or CPU problems, set `SFTPGATE_PPROF=127.0.0.1:6060` on a pod and
read `/debug/pprof/heap` through `kubectl port-forward`.
