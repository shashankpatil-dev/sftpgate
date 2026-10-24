"""Turns the load test's JSON results into one Markdown summary.

    python3 test/load/summary.py test/load/results/<run> > summary.md
"""
import json
import pathlib
import sys

ORDER = ["daily", "burst", "big", "limit", "chaos-pods", "chaos-storage", "soak"]


def load(directory):
    runs = []
    for name in ORDER:
        client = directory / f"{name}.client.json"
        if not client.exists():
            continue
        server = directory / f"{name}.server.json"
        restarts = directory / f"{name}.restarts.txt"
        runs.append({
            "name": name,
            "client": json.loads(client.read_text()),
            "server": json.loads(server.read_text()) if server.exists() else {},
            "restarts": restarts.read_text().strip() if restarts.exists() else "",
        })
    return runs


def op(report, name, field):
    return report["ops"].get(name, {}).get(field, "-")


def errors_total(report):
    return sum(sum(kinds.values()) for kinds in report["errors"].values())


def main():
    runs = load(pathlib.Path(sys.argv[1]))
    print("# Load test results\n")

    print("## Client side (what partners experience)\n")
    print("| Scenario | Sessions | File | Uploads | Upload/s | Upload p50 / p95 / p99 ms | Download p95 ms | Login p95 ms | MB/s up | Errors | Checksum mismatches | Half files |")
    print("|---|---|---|---|---|---|---|---|---|---|---|---|")
    for r in runs:
        c = r["client"]
        size = c["file_bytes"]
        size_s = f"{size // (1 << 20)}MiB" if size >= 1 << 20 else f"{size // 1024}KiB"
        print(f"| {r['name']} | {c['sessions']} | {size_s} | {op(c, 'upload', 'count')} | {op(c, 'upload', 'per_second')} | "
              f"{op(c, 'upload', 'p50_ms')} / {op(c, 'upload', 'p95_ms')} / {op(c, 'upload', 'p99_ms')} | "
              f"{op(c, 'download', 'p95_ms')} | {op(c, 'login', 'p95_ms')} | {c['upload_mb_per_s']} | "
              f"{errors_total(c)} | {c['checksum_mismatches']} | {c['partial_files_found']} |")

    print("\n## Server side (Prometheus)\n")
    print("| Scenario | Server upload p95 ms | Peak sessions | CPU max per pod (cores) | CPU avg total | Memory max per pod MB | Goroutines max | Storage errors | Rejected connections | Pod restarts |")
    print("|---|---|---|---|---|---|---|---|---|---|")
    for r in runs:
        s = r["server"]
        print(f"| {r['name']} | {s.get('server_upload_p95_ms')} | {s.get('sessions_peak')} | {s.get('cpu_cores_max_per_pod')} | "
              f"{s.get('cpu_cores_avg_total')} | {s.get('memory_mb_max_per_pod')} | {s.get('goroutines_max_per_pod')} | "
              f"{s.get('storage_errors')} | {s.get('connections_rejected')} | {r['restarts']} |")

    print("\n## Errors seen by the client\n")
    for r in runs:
        c = r["client"]
        if not c["errors"]:
            continue
        print(f"**{r['name']}**: failed uploads {c['failed_uploads']}, of which saved complete anyway {c['failed_but_saved_complete']}, half files {c['partial_files_found']}")
        for kind, msgs in c["errors"].items():
            for msg, n in sorted(msgs.items(), key=lambda x: -x[1])[:4]:
                print(f"- {kind}: {n} × `{msg}`")
        print()


if __name__ == "__main__":
    main()
