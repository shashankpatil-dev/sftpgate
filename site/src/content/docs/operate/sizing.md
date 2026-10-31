---
title: Sizing
description: How big to run it, based on measured load tests.
---

SFTP work is usually batch work: a few partners, once or twice a day, for minutes at a time. One small pod handles that easily.

| Your situation | Suggested setup |
|---|---|
| A few partners, files up to 100 MB | 1 pod, 1 CPU, 512 MiB (the chart default) |
| Up to 25 partners at once | The same. Raise CPU if upload times matter. |
| Files up to 500 MB to 1 GB | The same memory. Disk: largest `max_file_size` × uploads at once. |
| A machine failure must not stop partners | 2 to 3 replicas |
| More than 25 at once | Raise `max_connections`, and add CPU or replicas |

- **CPU sets the speed.** SSH encryption and checksums use it.
- **Memory stays flat,** whatever the file size. It depends on the number of transfers.
- **Keep `max_file_size` close to real file sizes.** Each upload reserves that much disk while it runs, so a 200 MiB limit for 256 KiB files makes uploads queue for space that's never used.

## Measured results

These ran on a kind cluster on one laptop (12 CPUs, 14 GB RAM), with SFTPgate limited to **1 CPU and 512 MiB per pod**. All parts shared one machine, so real clusters are faster. The comparisons, memory use and failure behavior carry over.

| Scenario | Files moved | Upload p95 | Errors | Peak memory |
|---|---|---|---|---|
| 10 sessions, 5 MiB files | 2,208 | 532 ms | 0 | 138 MB |
| 25 sessions, 256 KiB files | 13,184 | 140 ms | 0 | 83 MB |
| 5 sessions, 500 MiB files | 10 | 36.3 s | 0 | 183 MB |
| 35 sessions against a limit of 25 | 6,843 | 140 ms | Extra logins refused, as expected | 86 MB |
| 3 pods, 2 deleted mid-transfer | 56 | 27.8 s | 9, on the deleted pods | 225 MB |
| 2 pods, storage off for 20 s | 2,333 | 98 ms | Uploads during the outage, as expected | 74 MB |

Across all scenarios, about 30,000 files went up and came back down, with their SHA-256 compared. No corrupted file and no half file reached storage or a partner.

## Run it yourself

It needs Docker, kind, kubectl and helm, and about 15 GB of free disk.

```sh
test/load/run.sh                         # all scenarios, about 25 minutes
SCENARIOS="daily big" test/load/run.sh   # some of them
SCENARIOS=soak SOAK=1h test/load/run.sh  # a long run, to look for leaks
```

The [full report](https://github.com/shashankpatil-dev/sftpgate/blob/main/docs/LOAD-TEST.md) has every number and the bugs these tests found.
