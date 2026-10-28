# Example: receiving reconciliation reports from payment gateways

The second half of the revenue-assurance example. [REVENUE-ASSURANCE.md](REVENUE-ASSURANCE.md)
covered **sending** GST files out to EY. This page covers **receiving**:
payment gateways upload their daily reconciliation reports to us, each into its
own folder, and revenue-assurance is told the moment a file arrives.

Read the first guide first: the repository layout, one-time setup, CI and
"how Kubernetes picks up a change" are the same, so they aren't repeated here.

## The situation

- **Razorpay, Cashfree and PhonePe** each send a settlement reconciliation
  report every day: what they collected, their fees, and what they paid out.
- Most payment gateways deliver these by **uploading to the merchant's SFTP
  server**. That's us.
- **revenue-assurance** (Kotlin, Ktor) must match these reports against its own
  orders. It needs the files in its bucket, and a signal when each one arrives.

```
Razorpay ──┐                    SFTPgate                                 revenue-assurance
Cashfree ──┼─ SFTP upload ──►   checks who it is, saves the file   ──►   POST /internal/sftp/uploaded
PhonePe  ──┘   (port 22)        into gs://…-recon/<gateway>/incoming/    reads the file, reconciles
                                (PhonePe: decrypts it first)
```

Each gateway **logs in as itself** and **sees only its own folder**. Razorpay
can't see Cashfree's files, or even that they exist.

## 1. Where the files land

One new bucket, with one folder per gateway:

```
gs://acme-revenue-assurance-recon/
├── razorpay/incoming/    settlement_2026-10-24.csv
├── cashfree/incoming/    CF_recon_20261024.xlsx
└── phonepe/
    ├── incoming/         PP_settlement_20261024.csv.pgp   (as uploaded, still encrypted)
    └── decrypted/        2026-10-24/PP_settlement_20261024.csv.pgp   (readable copy)
```

What each gateway sees when it connects is just one folder:

```
sftp> ls
reports
sftp> ls reports
settlement_2026-10-24.csv
```

## 2. Add the gateways to `sftp/sftpgate.yaml`

This is the **same file** as the EY guide; you add `ra-recon` to `storages` and
an `accounts` section. Complete file:

```yaml
version: 1

server:
  host_keys: [/etc/sftpgate/keys/ssh_host_ed25519_key]
  timezone: Asia/Kolkata
  banner: "Acme revenue-assurance. Authorized partners only. All access is logged."
  max_connections: 25

storages:
  ra-recon: "gs://acme-revenue-assurance-recon"   # incoming reports (new)
  ra-gst: "gs://acme-revenue-assurance-gst"       # GST files for EY (outbound guide)

# Named actions: defined once, run by the hooks of each gateway's folder.
actions:
  # Razorpay's settlement CSV must have these columns and at least one row.
  razorpay-csv:
    check: { min_size: 100, csv: { header: [settlement_id, payment_id, amount, fee, tax, settled_at], min_rows: 1 } }
  # Cashfree sends Excel files, which can't be read as CSV: limits only.
  not-tiny:
    check: { min_size: 1KiB, max_size: 200MiB }
  phonepe-csv:
    check: { csv: { header: [transaction_id, amount, settlement_date], min_rows: 1 } }
  decrypt-phonepe:
    pgp_decrypt:
      key: "${file:/etc/sftpgate/secrets/pgp/ra-private.asc}"
      passphrase: "${env:RA_PGP_PASSPHRASE}"
      signer_keys: ["${file:/etc/sftpgate/secrets/pgp/phonepe-signing.pub.asc}"]
  phonepe-plain:
    copy: { storage: ra-recon, key: "phonepe/decrypted/{date}/{filename}" }
  notify-ra:
    webhook:
      url: "http://revenue-assurance.revenue-assurance.svc.cluster.local:8080/internal/sftp/uploaded"
      secret: "${env:RA_WEBHOOK_SECRET}"

accounts:
  # ---------------- Razorpay: key login, plain CSV ----------------
  - name: razorpay
    description: Razorpay settlement reconciliation reports
    auth:
      public_keys:
        - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl razorpay"
    allow_ips: ["203.0.113.0/24"]          # the IPs Razorpay gives you
    max_sessions: 3
    folders:
      - path: /reports
        storage: ra-recon
        prefix: razorpay/incoming/
        perms: [list, upload]
        patterns: ["*.csv"]
        max_file_size: 200MiB
        hooks:
          before_upload: [razorpay-csv]  # a broken file is refused at the door
          after_upload: [notify-ra]

  # ---------------- Cashfree: key AND password ----------------
  - name: cashfree
    description: Cashfree settlement reconciliation reports
    auth:
      mode: all
      public_keys:
        - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl cashfree"
      password_hash: "${env:CASHFREE_PW_HASH}"
    allow_ips: ["198.51.100.0/24"]
    max_sessions: 3
    folders:
      - path: /reports
        storage: ra-recon
        prefix: cashfree/incoming/
        perms: [list, upload]
        patterns: ["*.csv", "*.xlsx"]
        max_file_size: 200MiB
        hooks:
          before_upload: [not-tiny]
          after_upload: [notify-ra]

  # ---------------- PhonePe: PGP-encrypted files ----------------
  - name: phonepe
    description: PhonePe settlement reconciliation reports (PGP)
    auth:
      public_keys:
        - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl phonepe"
    allow_ips: ["192.0.2.0/24"]
    max_sessions: 3
    folders:
      - path: /reports
        storage: ra-recon
        prefix: phonepe/incoming/
        perms: [list, upload]
        patterns: ["*.pgp", "*.gpg"]
        max_file_size: 200MiB
        hooks:
          # Encrypted files can only be checked once decrypted.
          after_upload: [decrypt-phonepe, phonepe-csv, phonepe-plain, notify-ra]

# ---- Outbound to EY, unchanged from the first guide ----
remotes:
  ey:
    addr: sftp.ey.example:22
    user: acme-gst
    host_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
    private_key: "${file:/etc/sftpgate/secrets/ey/id_ed25519}"

transfers:
  - name: gst-to-ey
    schedule: "0 10,18 * * *"
    push:
      from: { storage: ra-gst, prefix: gst/outbox/, pattern: "*" }
      to: { remote: ey, path: /incoming }
      after: "move:gst/sent/"
```

The keys and IP ranges above are placeholders. Use the ones each gateway gives
you.

### What each part does

| Setting | Why it's there |
|---|---|
| **One account per gateway** | Each has its own login, IP list and folder. Revoking one gateway is deleting its block; the others aren't touched. |
| `allow_ips` | Only that gateway's servers can even try to log in. Payment gateways publish or send you their fixed outgoing IPs. **Ask each one.** |
| `max_sessions: 3` | One gateway can't use up all 25 connections. |
| `perms: [list, upload]` | They can upload and see what they sent. They **can't download, delete or overwrite**, so a leaked gateway key can't read or destroy your data. |
| No `overwrite` | Uploading a file name that already exists is refused. **Ask each gateway to use unique names** (with the date); a resend never silently replaces the original. |
| `patterns` | Only report files are accepted. Anything else is refused at upload. |
| `max_file_size: 200MiB` | Daily recon files are usually a few MB; this blocks a mistaken huge upload. |
| `mode: all` (Cashfree) | Shows key **and** password together, for a gateway that insists on both. Most use keys only. |
| `actions:` | Each piece of work is defined once, with a name, and every folder lists the ones it runs. All three gateways use the same `notify-ra`. |
| `before_upload` checks | A report with the wrong columns, no rows, or a tiny size is **refused while the gateway is still connected**, and never reaches revenue-assurance. The reason is in the audit log; tell the gateway "a refused upload means fix and resend", because most SFTP clients only show a generic failure. |
| `decrypt-phonepe` | Some gateways encrypt reports with your PGP key. SFTPgate decrypts them (and checks they're signed by PhonePe), so the Kotlin code never handles PGP. The uploaded file stays as it was, for audit. |
| `phonepe-csv` after decrypting | Encrypted files can't be checked at the door, so the check runs after decrypting. A bad file stops there: no plain copy, no webhook, and `SftpgateActionsGaveUp` fires. |
| `notify-ra` | Tells revenue-assurance straight away. No polling the bucket. |

## 3. What revenue-assurance receives

After each upload (and decryption, for PhonePe), SFTPgate sends:

```
POST /internal/sftp/uploaded
Content-Type: application/json
X-Sftpgate-Job: 6f1c…
X-Sftpgate-Signature: sha256=<hex HMAC-SHA256 of the body, using RA_WEBHOOK_SECRET>
```
```json
{
  "event": "upload",
  "job_id": "6f1c…",
  "account": "phonepe",
  "folder": "/reports",
  "storage": "ra-recon",
  "key": "phonepe/incoming/PP_settlement_20261024.csv.pgp",
  "filename": "PP_settlement_20261024.csv.pgp",
  "size": 482113,
  "sha256": "9b2e…",
  "uploaded": "2026-10-24T09:12:44+05:30",
  "copies": [
    { "storage": "ra-recon", "key": "phonepe/decrypted/2026-10-24/PP_settlement_20261024.csv.pgp" }
  ]
}
```

- **`account`** tells you which gateway, so which parser to use.
- **`key`** is the file as uploaded. **`copies`** lists files made by earlier
  steps; for PhonePe, **read `copies[0].key`**, the decrypted one.
- **Answer with any 2xx** (200, 202...) to say "got it". Anything else, or no
  answer, is **retried** with growing waits, up to `server.queue.max_attempts`.
- **Because of retries, the same `job_id` can arrive twice.** Ignore one
  you've already handled.

## 4. The Ktor endpoint (the only Kotlin code to write)

```kotlin
@Serializable
data class SftpFile(val storage: String, val key: String)

@Serializable
data class SftpUpload(
    val event: String,
    @SerialName("job_id") val jobId: String,
    val account: String,
    val key: String,
    val filename: String,
    val size: Long,
    val sha256: String,
    val copies: List<SftpFile> = emptyList(),
)

private val json = Json { ignoreUnknownKeys = true }

fun Route.sftpWebhook(secret: String, recon: ReconService, seen: ProcessedJobs) {
    post("/internal/sftp/uploaded") {
        val body = call.receive<ByteArray>()

        // 1. Is it really from SFTPgate? Compare signatures in constant time.
        val mac = Mac.getInstance("HmacSHA256").apply { init(SecretKeySpec(secret.toByteArray(), "HmacSHA256")) }
        val expected = "sha256=" + mac.doFinal(body).joinToString("") { "%02x".format(it) }
        val sent = call.request.headers["X-Sftpgate-Signature"].orEmpty()
        if (!MessageDigest.isEqual(expected.toByteArray(), sent.toByteArray())) {
            return@post call.respond(HttpStatusCode.Unauthorized)
        }

        val upload = json.decodeFromString<SftpUpload>(body.decodeToString())

        // 2. Already handled (a retry)? Say OK and do nothing.
        if (!seen.markIfNew(upload.jobId)) return@post call.respond(HttpStatusCode.OK)

        // 3. Queue the real work and answer quickly; don't reconcile inside the request.
        val fileKey = upload.copies.firstOrNull()?.key ?: upload.key   // PhonePe: the decrypted copy
        recon.enqueue(gateway = upload.account, bucketKey = fileKey, sha256 = upload.sha256)
        call.respond(HttpStatusCode.Accepted)
    }
}
```

- **Check the signature first.** Without it, anyone inside the cluster could
  fake "a file arrived".
- **`markIfNew`** is a small table or a Redis `SETNX` keyed by `job_id`.
- **Answer fast.** Reconciliation can take minutes; SFTPgate waits at most
  **10 seconds** for an answer, then retries. Do the work in a background worker that reads
  the file from GCS with the key.
- **Optional:** compare the file's SHA-256 with `sha256` after downloading, for
  end-to-end proof that it wasn't changed.

**Missed webhooks:** if revenue-assurance was down through every retry, the job
shows as **failed** in SFTPgate's UI and `SftpgateActionsGaveUp` fires, but
**the file is safe in the bucket**. A nightly job that lists
`<gateway>/incoming/` and processes anything not yet handled covers every case.

## 5. What changes in `sftp/values.yaml`

Compared with the EY-only setup, gateways now connect **in**, so the SFTP port
must be reachable from the internet, but only from them:

```yaml
service:
  sftp:
    type: LoadBalancer
    port: 22
    externalTrafficPolicy: Local        # keep the real gateway IP, so allow_ips works
    # A cloud firewall in front, with the same IPs as allow_ips: double protection.
    loadBalancerSourceRanges:
      - 203.0.113.0/24                  # Razorpay
      - 198.51.100.0/24                 # Cashfree
      - 192.0.2.0/24                    # PhonePe
    annotations:
      # A reserved static IP, so the address you give the gateways never changes.
      # GKE example; check your cloud's annotation name.
      networking.gke.io/load-balancer-ip-addresses: ra-sftp-ip

replicas: 2                             # a node failing mustn't stop uploads
podDisruptionBudget:
  enabled: true                         # cluster maintenance keeps 1 pod running

secretEnv:
  - ra-sftp-secrets                     # RA_WEBHOOK_SECRET, CASHFREE_PW_HASH, RA_PGP_PASSPHRASE
secretFiles:
  - secretName: ey-sftp-key
    mountPath: /etc/sftpgate/secrets/ey
  - secretName: ra-pgp
    mountPath: /etc/sftpgate/secrets/pgp   # ra-private.asc, phonepe-signing.pub.asc
```

- **Two replicas:** inbound uploads are what partners notice when they fail.
  Scheduled transfers still run on one pod only, so EY gets each file once.
- **Bucket access:** add **Storage Object User** on `acme-revenue-assurance-recon`
  for the same Google service account as in the first guide.
- **`RA_WEBHOOK_SECRET`** is the same value in SFTPgate's Secret and in the
  Kotlin service's config. Generate it once with `openssl rand -hex 32`.

## 6. Onboarding one gateway (repeat for each)

| Step | Who | What |
|---|---|---|
| 1 | You → gateway | Send the **address** (static IP or DNS name, port 22), the **user name** (`razorpay`), the **folder** (`/reports`), and **our host key fingerprint** (`ssh-keygen -lf ssh_host_ed25519_key.pub`), so they can be sure they're talking to us. |
| 2 | Gateway → you | Their **public SSH key**, their **outgoing IPs**, file **naming** and **format**, delivery **time**. For PGP: their **signing public key**; you send them **your PGP public key** to encrypt with. |
| 3 | You | A PR adding their account block and IPs (`sftpgate.yaml` + `values.yaml`). CI validates it, review, merge. Live in about a minute, **no restart**. |
| 4 | Both | A test upload. Check: the file is in `gs://…-recon/<gateway>/incoming/`, the audit log shows `upload` with `result: ok`, and revenue-assurance logged the webhook. |
| 5 | You | Add a "no file by 11:00" alert in your own monitoring, so a gateway that forgets is noticed the same day. |

**Removing a gateway:** delete its account block and merge. Its open sessions are
closed straight away, and its files stay in the bucket.

**Rotating a gateway's key:** add the new key next to the old one in
`public_keys`, let them switch, then remove the old one. Two small PRs, no
downtime.

## 7. Watching it

| Question | Where |
|---|---|
| Did Razorpay upload today? | Audit log: `event=upload account=razorpay`; or list `razorpay/incoming/` |
| Who is connected right now? | The web UI `/ui` (with `http.tokens`), or `sftpgate_sessions_active` |
| Did revenue-assurance get told? | Audit `action` events; UI "actions" page; alerts `SftpgateActionsRetrying` and `SftpgateActionsGaveUp` |
| Is someone guessing passwords? | `SftpgateLoginFailureSpike`, `SftpgateIPsBlocked` |
| Can SFTPgate reach the bucket? | `SftpgateStorageErrors`, `/readyz` |

Every upload's audit line records the **account, IP, session, file, size and
SHA-256**. That's the proof of "who sent what, when", which auditors ask for.
