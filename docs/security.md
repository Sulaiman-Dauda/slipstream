# Security model

Slipstream runs as root on the machine it manages. This page describes what it actually isolates, so
you can judge the risk rather than take a claim on trust.

To report a vulnerability, see [SECURITY.md](../SECURITY.md).

## Status

Slipstream is a **release candidate** and has had **no external security audit**. It has had one
deliberate internal adversarial pass, which found four real issues, all fixed and listed below.
Treat it accordingly: run it on servers you can rebuild, and keep backups off the box.

## Trust boundaries

| Boundary | Mechanism |
| --- | --- |
| Browser → panel | TLS. argon2id passwords with a timing-equalised verify for unknown accounts. Session tokens are opaque random values in the database — not JWTs — so they can be revoked. Cookies are `HttpOnly`, `Secure`, `SameSite=Strict`. Optional TOTP 2FA, each code usable once. Login rate-limited per address and account. The setup token is single-use and atomically consumed. |
| panel-api → panel-agent | Unix socket, mode `0660`, owner `root:slipstream`, plus a 48-byte shared secret compared in constant time. A fixed set of typed commands — never a shell string. |
| WordPress → panel | Per-site bearer token. A site's token can only purge URLs whose host matches that site's own domains. |
| Site → system | Dedicated Unix user, dedicated PHP-FPM pool and socket, `open_basedir` jail, `disable_functions`, private tmp and session directories, database user granted on exactly one database with a connection cap. |
| panel-api → system | **None.** It runs unprivileged, holds no capabilities and has no shell access. Every privileged action goes through the agent. |

## Per-site isolation

Every site gets:

- **its own Unix user** (`slip-site-<id>`), owning the site tree and nothing else;
- **its own PHP-FPM pool** running as that user, on its own socket;
- **an `open_basedir` jail** limited to the site root and `/usr/share/php`;
- **`disable_functions`** covering `exec`, `passthru`, `shell_exec`, `system`, `proc_open`, `popen`;
- **private temp, upload and session directories** inside the site, not `/tmp`;
- **its own database and database user**, granted only on that database;
- **its own APCu segment** — PHP-FPM allocates shared memory per user, not per service, so one site
  cannot enumerate another's cached objects;
- **chrooted SFTP** with no shell, unable to traverse above the site root.

The site root itself is owned `root:slip-site-<id>`, so the site user cannot replace the directory it
is jailed inside.

Verified on a live server, attempting each as a real site user: reading the agent token, connecting
to the agent socket, reading the panel database, listing another tenant's files, reading
`/etc/shadow`, and `sudo` — **all denied**.

### `/tmp` is deliberately excluded

`/tmp` is **not** on `open_basedir`. It is shared by every pool of a PHP version, so including it
would let one tenant read or plant files another wrote there, plus a symlink vector. Nothing needs
it: temp files, uploads and sessions are all redirected into the site's own directory. `PrivateTmp`
on the FPM unit would not help, because all pools fork from one master and share its namespace.

## Why the control plane is split

`panel-api` is the large, exposed surface: HTTP parsing, JSON decoding, session handling, the whole
API. `panel-agent` is small and does the privileged work.

A bug in the API — the place a bug is most likely — does not become root, because the API has no root
to give away. It can only ask the agent to perform one of a fixed set of typed operations, each of
which validates its own inputs independently.

The agent builds **argv arrays, never shell strings**. That is the boundary that stops a domain name
from becoming command injection, and it is not negotiable in contributions.

## Injection guards

| Input | Guard |
| --- | --- |
| Domains | `^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.…)+$`, enforced in both API and agent before any use in a path or config |
| Database identifiers | `^[a-z][a-z0-9_]{2,31}$`; names are generated (`site_<id>`), never user-supplied |
| System users | `^slip-site-[0-9]+$`; generated only |
| PHP versions | `^8\.[0-9]$`, **and** must have an installed FPM binary |
| Release IDs | `^[0-9]{8}-[0-9]{6}$` |
| File paths | resolved and confined to the site root, then opened with `openat2(RESOLVE_NO_SYMLINKS)` so the check cannot be raced |
| Archive extraction | every entry must resolve inside the destination; anything else is refused |
| Secrets | Restic and database passwords go through the environment or a `0600` defaults-file, never argv (argv is world-readable via `/proc`) |
| SQL console | runs as a throwaway account granted on one database; a read-only query must also be a single statement, so `select 1; drop table x` is refused |

Cron is the one intentional shell surface: commands are administrator-authored, run as the site's
unprivileged user, are time-bounded on manual execution and have their output capped.

## Findings from the internal review

Listed because knowing what *was* wrong tells you more about a codebase than a list of what is right.

| Severity | Issue | Fix |
| --- | --- | --- |
| High | The SQL console ran as the MariaDB superuser with the site's database only as the *default* schema, so an operator scoped to one site could read and write every other tenant's database by qualifying the table name. Reproduced: read another tenant's `wp_users` including password hashes, and created a table in their database. | Queries now run as a throwaway account granted on exactly one database and dropped afterwards. Grants, not SQL inspection, enforce the boundary. |
| Medium | Every login was recorded as `127.0.0.1`, because the API sits behind the panel's own nginx — so the audit trail could not answer "where did this session come from?", and rate limiting shared one bucket across all sources. | `X-Real-IP` is trusted when, and only when, the connection came from loopback. Deliberately the opposite of trusting forwarded headers from anywhere. |
| Medium | The file manager validated a path then opened it in a separate syscall, as root, in directories the tenant owns — a race that could redirect a root read or write outside the jail. The following `chown` followed symlinks too, so it could hand a tenant ownership of an arbitrary root file. | `openat2(RESOLVE_NO_SYMLINKS)` on the pre-resolved path, and `fchown` through the descriptor. |
| Low | TOTP codes were accepted anywhere in a ±1 step window with no record of use, so a captured code stayed valid for ~90 seconds. | The accepted step is recorded per user, and the watermark only moves forward. |

## Secrets at rest

`/var/lib/slipstream/state.db` is `0640 slipstream:slipstream` and holds session tokens, TOTP
secrets, per-site connector tokens and the backup repository password. Slipstream currently relies
on OS-level protection for these: they guard resources on the same machine, and an attacker who can
read that file already has the machine.

Site database passwords are **not** stored in panel state — they exist only in the application's own
configuration file.

## Network posture

- The API and the database bind to **loopback only**. nginx is the sole public ingress.
- The installer opens ports 22, 80, 443/tcp and 443/udp (for HTTP/3) and nothing else.
- nginx workers run as `www-data`, not root.
- Forwarded headers are not trusted from arbitrary sources, so IP-based controls cannot be bypassed
  with a spoofed header.

## Roles

| Role | Can |
| --- | --- |
| `admin` | everything, including server-wide operations. Effectively root on the machine. |
| `operator` | manage only the sites assigned to them; no server-wide views or settings |
| `readonly` | view everything, change nothing (but can still manage their own account) |

## Known limitations

Stated plainly so you can decide for yourself:

- **No external audit yet.** The most important limitation.
- **Two-factor authentication can be required on admin accounts.** Set `require_2fa_admin` (a
  checkbox on the settings page). While it is on, an admin with no second factor can reach only
  their own account and the enrolment routes, so turning it on cannot lock anyone out of the panel
  they need in order to satisfy it. It is off by default, and worth turning on wherever the panel is
  reachable from the internet.
- **A panel administrator is effectively root** by design. The `operator` and `readonly` roles limit
  what *those* accounts can do, not what an admin can.
- **A local user can reach the panel API on loopback.** Authentication is still required and login is
  rate-limited, but a compromised site could attempt logins locally without crossing your firewall.
- **Panel state is not encrypted at rest**, as above.
- **Drift detection covers only files the panel rendered.** It is not a file-integrity monitor.
- **The jail limits blast radius; it does not make bad application code safe.** An outdated WordPress
  plugin can still be exploited — the jail is what stops that becoming every other site's problem. A
  break *out* of the jail is a vulnerability; please report it.
