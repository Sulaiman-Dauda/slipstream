# Security model

## Trust boundaries

| Boundary | Mechanism |
|---|---|
| Browser → panel-api | TLS (self-signed at install, replaceable), argon2id passwords, HttpOnly/Secure/SameSite=Strict session cookies, single-use setup token |
| panel-api → panel-agent | Unix socket mode 0660 (root:slipstream) + 48-byte shared secret, constant-time compared |
| WordPress → panel-api | per-site bearer token; a site's token can only purge URLs whose host matches that site's own domains |
| Site → system | dedicated Unix user, `open_basedir`, `disable_functions`, private tmp/sessions, DB user scoped to one database with a connection cap |
| panel-api → system | none: `ProtectSystem=strict`, empty capability set, no shell access — all privileged work via typed agent commands |

## Injection surfaces and their guards

- **Domains** → `^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.…)+$` before any use in
  config paths or rendering; enforced in both API and agent.
- **DB identifiers** → `^[a-z][a-z0-9_]{2,31}$`; generated names only
  (`site_<id>`); passwords reject quote/backslash characters and never
  appear in argv (statement passed as a single argv element to `mariadb`).
- **System users** → `^slip-site-[0-9]+$`; generated only.
- **Release IDs** → `^[0-9]{8}-[0-9]{6}$`.
- **Restic passwords** → environment variable, never argv (argv is visible
  in /proc).
- **All command execution** → argv arrays through one `Runner` interface;
  `grep -rn "sh -c"` over the repo must stay empty.

## Secrets at rest

`/var/lib/slipstream/state.db` is 0640 slipstream:slipstream and holds
per-site DB passwords and connector tokens. v1 accepts OS-level protection
for these (they guard resources on the same host); at-rest encryption via a
keyfile is planned before multi-server support, where secrets cross
machines.

## Known v1 limitations (tracked, not hidden)

- Panel TLS is self-signed until the operator installs a real certificate.
- No 2FA yet; strongly recommend firewalling 8443 to trusted IPs.
- No rate limiting on `/api/login` beyond argon2 cost; same firewall advice.
- Drift detection is hash-based on managed files; it does not watch files
  the panel has never rendered.

## Reporting

security@slipstream.example — 90-day coordinated disclosure.
