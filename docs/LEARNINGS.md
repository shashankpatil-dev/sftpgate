# Learnings from load testing

The load test (see [LOAD-TEST.md](LOAD-TEST.md)) found real problems in
SFTPgate that the normal tests had missed. This page explains each one in
plain words: what happened, why, how it was fixed, and what to remember.

Everyday words used here:
- **Bucket:** the cloud storage where files live (S3, GCS, Azure).
- **Pod:** one running copy of SFTPgate in Kubernetes.
- **Partner:** whoever sends or fetches files over SFTP.

---

## 1. Large downloads came back with wrong data

**The most serious finding.** Data integrity is the whole point of the
product.

### What happened
When several partners downloaded large files (50 MiB) at the same time,
**19 of 20 files came back with different content** than what was stored.
The size was right; parts of the inside were wrong. One download at a time
was always fine, and so were small files.

### Why, in plain words
Think of a long book being read aloud to someone who asks for pages in a
random order: "page 12, now page 9, now page 13".

- SFTP clients like WinSCP and FileZilla do exactly that. To be fast, they ask
  for many 32 KB pieces at once, and the requests arrive out of order.
- The old SFTPgate opened **one long stream** from the bucket ("read from here
  to the end") and kept it as long as requests came in order. Each time a
  request jumped, it **threw the stream away** and opened a new one.
- With several big downloads at once, the bucket received a storm of streams
  opened and abandoned mid-way. It started **cutting streams short**.
- SFTPgate treated "stream cut short" as **"end of file"**. The partner's
  client then put the pieces it did get in the wrong places. Wrong data, right
  size.

### How it was proven
Each suspect was ruled out in turn:
- The stored files were correct, so uploads were fine.
- 200 parallel reads straight from the storage were all correct, so the
  storage was fine.
- 5 separate client programs also got wrong data, so the client was fine.
- What remained was SFTPgate's download code. It was then reproduced in a unit
  test without SSH at all.

### The fix
Files are now fetched in **fixed 1 MiB "windows"**, like reading the book one
chapter at a time:
- Each window is a **complete, bounded request**, read to the end, never
  abandoned halfway.
- The **last 3 windows are kept in memory**, so out-of-order page requests are
  answered from memory without asking the bucket again.
- If a window arrives short, it's **fetched again** (up to 3 times), and then
  the download **fails loudly** instead of quietly sending wrong data.

Result: 0 wrong files, and the same test ran about **8× faster**, because the
bucket no longer had to open and throw away hundreds of streams.

### What to remember
- **"End of stream" is not "end of file".** Always check the position against
  the real file size.
- **Bounded, complete requests are safer than long, reused streams** when
  requests come out of order.
- **Small-file tests hide this kind of bug.** It only showed up with large
  files and several transfers at once.

---

## 2. Uploads said "saved" when nothing was saved

### What happened
After the storage restarted, partners' uploads **succeeded**, but the files
**didn't exist**. Later downloads said "not found".

### Why, in plain words
It's like a courier saying "delivered" without anyone signing for the parcel.

The S3-compatible storage (SeaweedFS) answered **"200 OK, saved"** for uploads
into a bucket that didn't exist yet, and quietly kept nothing. SFTPgate
**trusted that answer** and told the partner the upload worked. For GST or
bank files, that means a file is lost while everyone believes it was
delivered.

### The fix
After every save, SFTPgate now **asks the bucket for the stored file's size
and checksum** and compares them with what it sent. That's the signature.
Only when they match does the partner hear "success". Otherwise the partner
gets a clear message: **"the storage did not keep the file, please upload it
again"**. It's counted as a storage error, so the alert fires.

This costs one small extra request per file and works for every storage type.

### What to remember
- **Don't trust a "saved" answer you can cheaply check.** Read it back.
- **Different storages behave differently in edge cases.** Some S3-compatible
  stores are looser than AWS S3. The gateway must protect the data itself.
- **Error messages matter.** The first version told partners "file does not
  exist", which is confusing. It now says what to do: upload again.

---

## 3. Pods were killed for using too much memory with large files

### What happened
With 50 MiB files, only about **7 uploads at once** pushed a pod past its
512 MiB memory limit, and Kubernetes killed it (`OOMKilled`). With 500 MB
files, this would have happened with just 1 or 2 partners.

### Why, in plain words
SFTPgate already saves each upload to its own disk first (the "spool"), so it
shouldn't need much memory. But the step that sends the file from disk to the
bucket is done by the cloud library (the AWS SDK), and that library **copies
the file into memory in big pieces** while sending: about **25 MB per file**,
without saying so.

Separately, Go (the language SFTPgate is written in) **didn't know** it was in
a 512 MiB container, so it let unused memory pile up instead of cleaning up
early.

A memory profile, a snapshot of what is using memory, showed it plainly:
**most of the memory was inside the AWS library's upload buffers.**

### The fix
- **Smaller pieces while sending:** 5 MiB (the smallest S3 allows), one piece
  at a time per file.
- **At most 4 files are sent to the bucket at the same moment** per pod.
  Partners still upload in parallel to the disk; only the final send waits its
  turn. For batch SFTP work that adds seconds at most.
- **Smaller download windows:** 1 MiB each, see issue 1.
- **Go now knows the container's limit:** at start-up SFTPgate reads it and
  tells Go to stay under 90% of it.

Result: **25 partners × 50 MiB** in one 512 MiB pod with no crash, and five
**500 MiB** files at once with memory under **190 MB**. **Memory now depends
on how many transfers run, not on how big the files are.**

### What to remember
- **Libraries can hide large memory use.** Measure with a profile; don't guess.
- **Cap the expensive step,** not the whole system: uploads stay parallel and
  only the send-to-bucket step is limited.
- **Programs in containers should know their memory limit.**

---

## 4. The "how long did it take" numbers were useless

### What happened
The server's own p95 transfer times (how long the slowest 5% took) were wrong
and couldn't be compared with what partners actually experienced.

### Why, in plain words
Imagine timing runners with only three markers: "under 50 ms", "under 200 ms"
and "under 800 ms". Everyone lands in the first one, so you learn nothing.

The metric's time slots ("buckets") were far too wide, so nearly every
transfer fell into the first slot.

### The fix
Finer slots, from **10 ms up to about 4 minutes**, each 2.5× the last. Both
small and large files now land in a slot that says something useful.

### What to remember
- **Check that your metrics can actually answer your questions** before you
  rely on them. The load test showed the gap between what the server reported
  and what clients measured.

---

## 5. The documentation promised more than was true

### What happened
The docs said "the bucket checks an MD5 fingerprint on every save". While
investigating issue 3, the S3 library's code turned out to say: *"S3 doesn't
support ContentMD5 validation anymore."*

### The fix
The docs now say exactly what happens:
- GCS and Azure check the MD5.
- S3 checks each request with its own checksums.
- SFTPgate now reads every save back itself (issue 2), on all storages.

### What to remember
- **Verify claims in docs against the code,** especially security and
  integrity claims.

---

## 6. A setting that looks harmless can block uploads

**Not a bug, but a real lesson for anyone configuring SFTPgate.**

### What happened
Some uploads were refused with "out of upload space" even though the disk had
plenty of room.

### Why, in plain words
To guarantee an upload never fails halfway for lack of disk, SFTPgate
**reserves the folder's `max_file_size`** on disk when the upload starts. The
test folder allowed 200 MiB files, but the real files were 256 KiB. So 50
partners reserved 50 × 200 MiB = 10 GB for files that needed about 13 MB in
total.

### What to remember
- **Set `max_file_size` close to the real file sizes,** not "as big as
  possible".
- Size the disk as roughly **largest `max_file_size` × uploads at the same
  time**.

---

## Lessons about testing itself

The test set-up had its own problems. None of them was an SFTPgate bug, but
each one taught something.

| What went wrong | Lesson |
|---|---|
| The first S3 mock (`adobe/s3mock`) corrupted itself under load and answered everything with errors | Test tools have bugs too. Use one that behaves like production (SeaweedFS). |
| The test storage kept the space of deleted files and filled the laptop's disk | Cap test storage, wipe it between scenarios, and watch free disk while testing (a "disk guard"). |
| The GCS emulator stops listings at 1,000 files | Emulators aren't the real cloud. Check limits on the real service before trusting them. |
| A check passed against an old copy of SFTPgate still running on the same port | Make sure you're testing the build you think you're testing. |
| `curl ... \| grep -q` checks failed at random in CI | With `pipefail`, `grep -q` stopping early can fail the pipeline. Read the whole response first. |
| 200 sessions per pod was the first target | Test what really happens. SFTP work is a few partners at a time, so reliability matters more than huge concurrency. The limit is now `max_connections: 25`. |

## The most important takeaways

1. **The guarantee "no half files, no wrong files" held in every scenario**,
   including pod crashes and a storage outage. But it only became true for
   large concurrent downloads after the load test found issue 1.
2. **Trust nothing you can check cheaply:** read back saves, compare sizes,
   verify checksums.
3. **Measure memory and data correctness with real file sizes and several
   transfers at once.** Small, one-at-a-time tests hid all three serious bugs.
4. **Test the failure cases on purpose** (kill pods, stop the storage). That's
   where the "saved but lost" bug appeared.
