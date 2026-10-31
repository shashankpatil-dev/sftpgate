# Security policy

SFTPgate carries confidential files (tax filings, bank and payment data), so
security reports are taken seriously and handled privately.

## Reporting a vulnerability

**Please don't open a public issue for a security problem.**

Report it privately through GitHub: open the repository's **Security** tab and
choose **Report a vulnerability**. Include:

- what the problem is and what an attacker could do with it;
- steps to reproduce it, or a proof of concept;
- the SFTPgate version (`sftpgate version`) and the relevant config, with
  secrets removed.

What to expect:

| When | What happens |
|---|---|
| within 3 working days | we confirm we received the report |
| within 10 working days | we confirm whether it's a vulnerability and how serious it is |
| when a fix is ready | a patched release, then a public advisory crediting you (unless you prefer not) |

Please give us a reasonable time to release a fix before disclosing the
problem publicly.

## Supported versions

Security fixes go into the latest minor release. Upgrade to it to receive them.

| Version | Supported |
|---|---|
| 0.4.x | ✅ |
| older | ❌ |

## In scope

- Getting around authentication, IP allowlists, the login throttle or
  connection limits.
- Reaching files outside an account's folders (the folder jail), or doing
  operations its permissions don't allow.
- Secrets leaking: through logs, the API, traces, error messages,
  or object metadata.
- Weak cryptography being accepted (SHA-1 must always be refused).
- Files being corrupted, lost, or reported as saved when they weren't.
- The read-only API changing anything, or being usable without a valid
  token; an admin call working when it isn't allowed in `http.admin.allow`,
  or with anything but an admin token.
- Problems in the container image or Helm chart defaults (privileges,
  writable filesystem, exposed endpoints).

## Out of scope

- Problems that need someone who can already change the config in git, read
  the host's secrets or run code in the cluster.
- Missing protections that the docs mark as your platform's job (network
  firewalls, the TLS Ingress in front of the API, log retention, malware
  scanning).
- Denial of service through sheer traffic volume. The built-in limits and
  allowlists reduce it, but they can't replace network-level protection.

## Built-in protections

See the security section of the [design (HLD)](docs/HLD.md) and
[What you get](docs/WHAT-YOU-GET.md). In short:

- SFTP only: no shell, commands or port forwarding.
- No SHA-1 anywhere.
- Secrets only as references, and bcrypt password hashes.
- Every save is read back before success is reported.
- Distroless, non-root, read-only container.
- Every access is audited.
