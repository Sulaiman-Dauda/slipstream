# API reference

The panel is driven entirely by this HTTP API — the web UI is just a client of it. It listens on
`127.0.0.1:5252` and is reached through nginx.

## Authentication

`POST /api/login` with an email and password sets a session cookie. Send that cookie with every
subsequent request.

```bash
curl -sk -c jar -b jar https://your-panel/api/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"your-password"}'

curl -sk -c jar -b jar https://your-panel/api/sites
```

The cookie is `HttpOnly`, `Secure` and `SameSite=Strict`, and the token is an opaque random value
stored in the database, so sessions can be revoked. Sessions last 24 hours.

If the account has 2FA, the first response is `401` with `{"totp_required": true}`; repeat the
request including `"totp":"123456"`. Each code works once.

## Roles

Every endpoint below is marked with the role it needs.

| Marker | Requires |
| --- | --- |
| **open** | no authentication |
| **auth** | any signed-in account |
| **manage** | `admin` or `operator` (operators only for their own sites) |
| **global** | `admin` or `readonly` — not `operator`, as these are server-wide |
| **admin** | `admin` only |

An `operator` sees and touches only assigned sites; requests for others return `404`, not `403`, so
the API does not confirm that a site exists.

## Conventions

- JSON in, JSON out.
- Long operations return **`202`** with a task; poll the resource or `GET /api/tasks/{id}`.
- Errors are `{"error":"..."}` with a meaningful status code.
- Request bodies are capped at 4 MB and unknown fields are rejected.

## Setup and session

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/bootstrap` | open | whether initial setup is complete |
| `POST` | `/api/setup` | open | consume the one-time token, create the first admin |
| `POST` | `/api/login` | open | sign in |
| `POST` | `/api/logout` | auth | sign out and delete the session |
| `GET` | `/api/me` | auth | the current account |
| `GET` | `/healthz` | open | liveness |

## Account

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `POST` | `/api/account/password` | auth | change your own password |
| `POST` | `/api/account/2fa/begin` | auth | start TOTP enrolment; returns the secret |
| `POST` | `/api/account/2fa/confirm` | auth | confirm with a code and switch 2FA on |
| `POST` | `/api/account/2fa/disable` | auth | disable 2FA (requires your password) |
| `GET` | `/api/account/sessions` | auth | your active sessions |
| `DELETE` | `/api/account/sessions/{id}` | auth | revoke one |

## Users

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/users` | global | list accounts |
| `POST` | `/api/users` | admin | create one — `role` is `admin`, `operator` or `readonly`; an operator needs `site_ids` |
| `DELETE` | `/api/users/{id}` | admin | delete an account |

## Sites

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/sites` | auth | list (scoped to the caller) |
| `POST` | `/api/sites` | manage | create — returns `202` and a task |
| `GET` | `/api/sites/{id}` | auth | one site |
| `DELETE` | `/api/sites/{id}` | manage | delete — returns `202` |
| `PUT` | `/api/sites/{id}/config` | manage | update the site configuration |
| `PUT` | `/api/sites/{id}/php` | manage | change PHP version or limits |
| `POST` | `/api/sites/{id}/certificate` | manage | issue a Let's Encrypt certificate |
| `POST` | `/api/sites/{id}/purge` | manage | purge the page cache |
| `POST` | `/api/sites/{id}/warm` | manage | crawl the sitemap to warm the cache |
| `GET` | `/api/sites/{id}/cache-stats` | auth | object-cache hit rate and memory |
| `POST` | `/api/sites/{id}/migration` | manage | import an existing site from an archive + SQL dump |

`POST /api/sites/{id}/migration` takes `{"archive": "...", "sql": "...", "old_domain": "...",
"confirm": "<destination domain>"}`. `archive` and `sql` are paths **inside the destination site's
directory** — upload them over SFTP first. `old_domain` drives the search-and-replace through the
imported database, and `confirm` must equal the destination domain or the request is rejected.

Creating a site:

```bash
curl -sk -c jar -b jar https://your-panel/api/sites \
  -H 'Content-Type: application/json' \
  -d '{"domain":"shop.example.com","type":"woocommerce","profile":"commerce",
       "admin_email":"you@example.com","admin_user":"you",
       "admin_password":"strong-password","object_cache":true}'
```

## Releases, staging and Safe Push

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/sites/{id}/deployments` | auth | deployment history with Guard verdicts |
| `POST` | `/api/sites/{id}/deployments` | manage | create a release |
| `POST` | `/api/deployments/{id}/promote` | manage | promote a specific release |
| `POST` | `/api/sites/{id}/rollback` | manage | roll back to the previous release |
| `POST` | `/api/sites/{id}/staging` | manage | clone production to staging |
| `GET` | `/api/sites/{id}/staging/tables` | manage | what a database sync would copy |
| `POST` | `/api/sites/{id}/staging/database` | manage | re-sync the staging database |
| `POST` | `/api/sites/{id}/safe-push` | manage | measure and promote if it holds up |

## Backups

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/sites/{id}/backups` | auth | list snapshots |
| `POST` | `/api/sites/{id}/backups` | manage | take one — `kind` is `full`, `files` or `database` |
| `POST` | `/api/backups/{id}/verify` | manage | restore it to scratch and check it |
| `POST` | `/api/backups/{id}/restore` | manage | restore, in place or to a target directory |
| `POST` | `/api/backups/test` | admin | check the repository is reachable |

## Files

Jailed to the site root. Paths are resolved and then opened with `openat2(RESOLVE_NO_SYMLINKS)`, so
a symlink cannot redirect the operation outside the site.

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/sites/{id}/files?path=` | auth | list a directory |
| `GET` | `/api/sites/{id}/files/read?path=` | auth | read a text file (2 MB cap) |
| `POST` | `/api/sites/{id}/files/write` | manage | write a text file |
| `GET` | `/api/sites/{id}/files/download?path=` | auth | download (16 MB cap; use SFTP above that) |
| `POST` | `/api/sites/{id}/files/upload` | manage | upload (16 MB cap) |
| `POST` | `/api/sites/{id}/files/manage` | manage | delete, rename, mkdir |

## Database

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/sites/{id}/database` | auth | tables, row counts, sizes |
| `POST` | `/api/sites/{id}/database/query` | manage | run SQL |
| `POST` | `/api/sites/{id}/database/export` | manage | dump to the site's `shared/exports/` |
| `POST` | `/api/sites/{id}/database/import` | manage | import a `.sql` from inside the site |
| `POST` | `/api/sites/{id}/database/adminer` | manage | launch a time-limited Adminer session |

The console is **read-only unless you pass `"allow_writes": true`**, and a read-only query must be a
single statement — `select 1; drop table x` is refused. Queries run as a throwaway account granted on
that site's database only, so cross-database access fails at the server.

## WordPress

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `POST` | `/api/sites/{id}/wp/login` | manage | one-click login URL (single-use, 5 minutes) |
| `GET` | `/api/sites/{id}/wp/plugins` | auth | plugins and themes with available updates |
| `POST` | `/api/sites/{id}/wp/update` | manage | update `core`, `plugins`, `themes` or `all` |
| `POST` | `/api/sites/{id}/wp/object-cache` | manage | enable or disable the object cache |

## SFTP and SSH keys

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `POST` | `/api/sites/{id}/sftp` | manage | enable SFTP and set the password |
| `GET` | `/api/sites/{id}/ssh-keys` | auth | list authorised keys |
| `POST` | `/api/sites/{id}/ssh-keys` | manage | add a key |
| `DELETE` | `/api/sites/{id}/ssh-keys/{fingerprint}` | manage | remove one |

## Cron

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/sites/{id}/cron` | auth | this site's jobs |
| `POST` | `/api/sites/{id}/cron` | manage | create one |
| `DELETE` | `/api/cron/{id}` | manage | delete one |
| `POST` | `/api/cron/{id}/run` | manage | run it now |

## Server

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/system/status` | global | CPU, memory, disk headroom, service state |
| `GET` | `/api/system/metrics` | global | 24 hours of capacity samples |
| `GET` | `/api/system/drift` | global | managed files that no longer match |
| `POST` | `/api/system/drift/{id}/resolve` | admin | `{"action":"restore"}` or `{"action":"accept"}` |
| `GET` | `/api/services` | global | nginx, PHP-FPM, MariaDB, Redis state |
| `POST` | `/api/services/{name}/restart` | admin | restart one |
| `GET` | `/api/logs` | global | tail a server log |
| `GET` | `/api/firewall` | global | ufw rules |
| `POST` | `/api/firewall/rule` | admin | add or remove a rule |
| `GET` | `/api/audit` | global | audit log — who did what, from where |
| `GET` | `/api/settings` | global | panel settings |
| `PUT` | `/api/settings` | admin | update them |
| `POST` | `/api/panel/certificate` | admin | install a certificate for the panel itself |
| `POST` | `/api/panel/update` | admin | self-update, health-checked with automatic rollback |

`POST /api/panel/update` takes `{"base_url": "https://…", "version": "v0.2.0"}`. `base_url` is the
release root holding the binaries and their `.sha256` files; it must be `https://` and is required
in practice. See [Operations](./operations.md#upgrades).

## Tasks and events

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/tasks` | auth | recent tasks |
| `GET` | `/api/tasks/{id}` | auth | one task with its log |
| `GET` | `/api/events` | auth | server-sent events stream for live updates |

## Connector

| Method | Path | Role | Purpose |
| --- | --- | --- | --- |
| `POST` | `/api/connector/purge` | site token | called by the WordPress mu-plugin |

Authenticated with a per-site bearer token, not a session. A site's token can only purge URLs whose
host matches that site's own domains.
