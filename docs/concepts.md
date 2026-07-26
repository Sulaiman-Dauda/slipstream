# Concepts

Six ideas explain most of how Slipstream behaves. Read this once and the rest of the panel stops
being surprising.

## Desired state

The panel's SQLite database at `/var/lib/slipstream/state.db` is the **single source of truth**.
It holds what each site *should* look like: its type, PHP version, cache policy, resource budget,
backup policy.

Nothing on the filesystem is authoritative. nginx vhosts, PHP-FPM pools and cache-zone
definitions are **rendered** from desired state, and the render is deterministic — the same state
always produces the same files.

Two consequences worth knowing:

- Changing a setting does not patch a config file; it updates state and re-renders. That is why
  there is no "advanced settings, edit at your own risk" textarea.
- Because the panel knows exactly what it wrote, it can tell when something else changed it. See
  **drift** below.

## Sites

A site is a domain plus a type plus a configuration. Creating one provisions, in this order:

1. a dedicated Unix user, `slip-site-<id>`, that owns the site and nothing else;
2. the directory layout under `/srv/sites/<domain>/`;
3. a MariaDB database and a user granted only on that database;
4. a PHP-FPM pool with its own socket, `open_basedir` jail and calculated worker count;
5. the application itself, for the types that have one;
6. the rendered nginx vhost, with caching already on;
7. a certificate — self-signed at first, replaced when you issue a real one.

If any step fails, the whole thing is rolled back: the user, database and directory tree are
removed. A half-created site that cannot be recreated is worse than no site.

See [Sites](./sites.md) for the types.

## Releases

Code lives in **immutable releases**, and the document root is a symlink:

```
/srv/sites/example.com/
├── current -> releases/20260726-151844      ← the symlink nginx serves
├── releases/
│   ├── initial/
│   ├── 20260726-104233/
│   └── 20260726-151844/
├── shared/                                   ← survives deployments
│   ├── uploads/          symlinked into each release
│   └── wp-config.php     symlinked into each release
├── logs/
└── tmp/sessions/
```

A deployment writes a new release directory and then moves the symlink. That makes promotion and
rollback **atomic** — there is no window where half the old code and half the new code are being
served — and rollback is just pointing the symlink back.

Anything that must survive a deployment (uploads, configuration with credentials in it) lives in
`shared/` and is symlinked into each release. Credentials never live inside a release, so a release
is safe to copy around.

## Profiles

A site's **profile** sets its caching posture. It is the one knob most people need, instead of a
page of individual cache settings.

| Profile | Page TTL | Stale window | Suits |
| --- | --- | --- | --- |
| **Balanced** | 10 minutes | 1 hour | blogs, brochure sites, most WordPress |
| **Commerce** | 5 minutes | 1 hour | WooCommerce and anything with a cart |
| **Maximum** | 24 hours | 3 days | documentation, landing pages, rarely-changing content |

Commerce is not simply "less caching" — it also switches on the object cache by default and
suppresses WooCommerce's cart-fragments AJAX call, which otherwise fires on every anonymous page
view and punches straight through the page cache.

## The two processes

```
browser ──▶ nginx :443 ──▶ panel-api  (user: slipstream, no root)
                              │  SQLite desired state
                              ▼  Unix socket + shared secret, typed commands
                          panel-agent (root)
                              ▼
                     nginx · PHP-FPM · MariaDB · Restic · certbot
```

`panel-api` serves the HTTP API and the web UI. It runs unprivileged and **cannot change the
system**. Everything privileged is a typed command — `CreateSite`, `DeployRelease`,
`RestoreSnapshot` — sent to `panel-agent` over a root-owned socket authenticated with a shared
secret.

The agent is **stateless**: desired state arrives with each command. It builds argv arrays and
never shell strings, which is the boundary that stops a domain name from becoming command
injection.

Why split it: a bug in the HTTP layer — the largest and most exposed surface — does not
automatically become root, because the API has no root to give away. See
[Security model](./security.md).

## Drift

Every file the panel renders is hashed and the hash recorded. The drift checker compares the
filesystem against those records.

If you hand-edit a managed nginx vhost, Slipstream **notices and tells you**. It does not silently
overwrite your change on the next render, and it does not silently keep your change either — it
surfaces the difference and lets you choose:

- **restore** — discard the manual edit and re-render from desired state;
- **accept** — record the new hash as expected (useful when you edited deliberately).

Drift only covers files the panel rendered. It is not a general file-integrity monitor.

## Tasks

Anything that takes more than a moment — provisioning, backups, restores, staging clones — runs as
a **task** with a status, a progress value and a log. The API returns immediately with a task ID
and the UI streams updates over server-sent events.

This matters when scripting: `POST /api/sites` returns `202` with a task, not a finished site.
Poll `GET /api/sites/{id}` until `status` is `active`, which is what `scripts/e2e-verify.sh` does.

## Where to next

- [Sites](./sites.md) — the site types in detail.
- [Caching](./caching.md) — what the profiles actually do.
- [Staging and Safe Push](./staging-and-safe-push.md) — how releases and Performance Guard combine.
