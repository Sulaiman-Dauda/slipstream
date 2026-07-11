# Slipstream architecture

## Two-process control plane

```
browser ──HTTPS:8443──▶ panel-api (user: slipstream, sandboxed)
                            │  SQLite: /var/lib/slipstream/state.db
                            │  desired state, tasks, audit, drift hashes
                            ▼  Unix socket + shared secret, typed commands
                        panel-agent (root)
                            │  useradd, nginx render+reload, php-fpm pools,
                            │  mariadb, wp-cli, restic, certbot, purges
                            ▼
                     the data plane serving visitors
```

Design rules:

1. **panel-api never runs shell commands.** It manipulates desired state
   and calls typed agent commands (`CreateSite`, `DeployRelease`, …).
2. **panel-agent is stateless.** Desired state arrives with each command;
   the agent renders configuration from it and executes argv arrays only —
   no string-built shell anywhere.
3. **SQLite is the single source of truth.** Rendered files are hashed and
   recorded; the drift checker compares reality against the record and
   surfaces differences instead of silently overwriting them.

## Site layout

```
/srv/sites/example.com/
├── current -> releases/20260711-151844     # atomic promote/rollback
├── releases/…                              # immutable code releases
├── shared/uploads/                         # persistent data, symlinked in
├── logs/                                   # php-error.log, db-latest.sql
└── tmp/sessions/                           # private tmp, sessions
```

Each site: dedicated Unix user (`slip-site-<id>`), dedicated PHP-FPM pool +
socket, `open_basedir` jail, dedicated MariaDB database and user with a
connection cap, optional Redis namespace, per-site cache directory.

## Velocity Engine

Rendered into Nginx per site, per profile (Balanced / Commerce / Maximum):

- FastCGI full-page cache for anonymous traffic
- bypass rules: POST, query strings, login/cart/checkout/admin cookies & URIs
- request coalescing (`fastcgi_cache_lock`)
- stale-while-revalidate + stale-if-error (`fastcgi_cache_use_stale`,
  `fastcgi_cache_background_update`)
- precise invalidation: the mu-plugin connector reports content changes;
  the agent deletes exact cache entries by key hash (no purge module, no
  full flushes)
- calculated OPcache and worker counts per pool; calculated InnoDB buffer
  pool per machine role

## Performance Guard

`POST /api/sites/{id}/safe-push`:

1. probe production (baseline) and staging (candidate) over loopback with
   Host headers, warmup discarded
2. compare p50/p95, error rate, DB queries and peak memory (connector
   footer metrics)
3. verdicts: pass → deploy staging code to production as an immutable
   release and promote; warn → require `force`; block → refuse, report
   stored on the deployment record

## Recovery

Restic snapshots (files + logical DB dump in one snapshot) to any
S3-compatible repository, encrypted client-side. Verified restore = actual
restore into a scratch dir + repo check + tree/dump sanity + measured
duration shown in the UI as the recovery estimate.

## What v1 deliberately does not do

Email, DNS, domain registration, billing, resellers, Docker/Kubernetes,
file manager, in-browser code editor, app marketplace. The multi-server
module and Caddy/FrankenPHP engine plug in behind existing abstractions
(`engine.Renderer`, node-role fields in state) when benchmarks justify them.
