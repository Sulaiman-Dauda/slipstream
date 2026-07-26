# Architecture

## Request path

```
                      visitor
                         │
                    nginx :443            ← TLS, HTTP/2 (HTTP/3 where available)
                         │
        ┌────────────────┴────────────────┐
        │                                 │
   page cache HIT                   cache MISS / BYPASS
   served from disk,                      │
   no PHP involved                   PHP-FPM (per-site pool, user, jail)
                                          │
                                     MariaDB  ·  APCu object cache
```

A cache hit never reaches PHP. That single fact accounts for most of the performance difference in
[the benchmarks](./benchmarks.md).

## Control plane

```
browser ──nginx :443──▶ panel-api  (user: slipstream, unprivileged)
                            │  SQLite /var/lib/slipstream/state.db
                            │  desired state · tasks · audit · drift hashes
                            ▼  Unix socket 0660 root:slipstream + shared secret
                        panel-agent  (root)
                            │  useradd · nginx render+reload · FPM pools
                            │  MariaDB · wp-cli · restic · certbot · purges
                            ▼
                        the data plane
```

`panel-api` listens on `127.0.0.1:5252` and is reached only through nginx, which is the sole public
ingress.

### Three design rules

1. **panel-api never runs shell commands.** It manipulates desired state and calls typed agent
   commands (`CreateSite`, `DeployRelease`, `RestoreSnapshot`, …). It holds no root and no
   capabilities, so a bug in the HTTP layer cannot become a system compromise.
2. **panel-agent is stateless.** Desired state arrives with each command. The agent renders
   configuration from it and executes **argv arrays only** — there is no string-built shell anywhere
   in the privileged path.
3. **SQLite is the single source of truth.** Rendered files are hashed and recorded; the drift
   checker compares reality against the record and surfaces differences rather than silently
   overwriting them.

## Site layout

```
/srv/sites/example.com/
├── current -> releases/20260726-151844     # atomic promote and rollback
├── releases/…                              # immutable code releases
├── shared/
│   ├── uploads/                            # persistent data, symlinked in
│   └── wp-config.php                       # credentials never live in a release
├── logs/                                   # php-error.log, db-latest.sql
└── tmp/sessions/                           # this site's private tmp and sessions
```

Per site: a dedicated Unix user (`slip-site-<id>`), a dedicated PHP-FPM pool and socket, an
`open_basedir` jail, a dedicated MariaDB database and user with a connection cap, its own APCu
segment, and its own cache directory.

## The Velocity Engine

Rendered into nginx per site, per profile (Balanced / Commerce / Maximum):

- FastCGI full-page cache for anonymous traffic;
- bypass rules for POST, query strings, and login/cart/checkout/admin cookies and URIs;
- request coalescing (`fastcgi_cache_lock`) so an expired hot page is regenerated once, not 500
  times;
- stale-while-revalidate and stale-if-error (`fastcgi_cache_use_stale`,
  `fastcgi_cache_background_update`);
- **pre-compressed storage** — PHP compresses its own output, nginx caches the compressed body, and
  the cache key includes a normalised `Accept-Encoding` bucket so hits are served verbatim with no
  per-request gzip;
- precise invalidation: the WordPress mu-plugin reports content changes and the agent deletes exactly
  the affected cache entries. No purge module, no full flushes;
- `open_file_cache` for static assets, so hot files are not re-opened and re-stat'ed per request.

Sizing is calculated, not fixed: OPcache and worker counts per pool from the site's memory budget,
and the InnoDB buffer pool from the machine.

## Performance Guard

`POST /api/sites/{id}/safe-push`:

1. probe production (baseline) and staging (candidate) over loopback with the correct `Host` header,
   discarding warm-up;
2. compare p50/p95 latency, error rate, database query count and peak memory — the last two from the
   connector's own per-request metrics;
3. verdict: **pass** deploys staging's code to production as an immutable release and promotes it;
   **warn** requires an explicit `force`; **block** refuses and stores the report on the deployment
   record.

## Recovery

Restic snapshots — site tree plus a logical database dump, captured together in one snapshot — to any
supported backend, encrypted client-side. A **verified restore** is an actual restore into a scratch
directory plus a repository check, a tree and dump sanity check, and a measured duration that becomes
the recovery-time estimate shown in the panel.

## Repository layout

```
cmd/panel-api      unprivileged API server (embeds ui/dist)
cmd/panel-agent    privileged agent daemon
cmd/slipctl        command-line client
internal/api       HTTP handlers, auth, RBAC, SSE
internal/agent     provisioning, releases, staging, backups, certs, files, drift
internal/rpc       the API↔agent socket protocol
internal/state     SQLite desired state, migrations, models
internal/engine    web-server abstraction + the nginx renderer
internal/phpfpm    FPM pool renderer, OPcache and worker sizing
internal/tune      hardware probe and MariaDB auto-tuner
internal/velocity  cache policy and purge logic
internal/guard     Performance Guard regression gate
internal/sysprobe  memory, CPU and disk facts
ui/                React + TypeScript + Vite panel UI
connector/         WordPress mu-plugin: precise purge, metrics, magic login
installer/         install.sh + systemd units
scripts/           e2e-verify.sh and release tooling
bench/             benchmark suite
docs/              this documentation
```

## Deliberate non-goals

Slipstream does not do email hosting, DNS, domain registration, billing, resellers, or
Docker/Kubernetes orchestration. Each of those is a product in its own right, and a panel that does
all of them badly is worse than one that does hosting well.

Multi-server support and an alternative engine (Caddy or FrankenPHP) plug in behind the existing
`engine.Renderer` abstraction and node-role fields in state, if and when benchmarks justify them.

## Dependencies

Deliberately few. SQLite for state — no separate database server. No Redis unless a site asks for it,
no message broker, no ORM, no search engine. The Go binaries are static, so there is no runtime to
install and nothing to keep in step with the OS.
