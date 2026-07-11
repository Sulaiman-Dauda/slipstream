# Slipstream

**The fastest way to run a website — and the only panel that keeps it fast.**

Slipstream is a performance-first hosting control plane. One command to install,
one click to launch a production-ready site. Every site is automatically cached,
tuned, isolated, and backed up off-site with verified restores. Every deployment
is performance-tested before it goes live.

## Promises

1. **Launch quickly** — one-command install, one-click WordPress/WooCommerce/PHP/static/proxy sites.
2. **Run quickly** — Velocity Engine: full-page caching with precise invalidation, request
   coalescing, stale-while-revalidate, auto-sized OPcache and InnoDB buffer pool.
3. **Change safely** — one-click staging, immutable releases, instant rollback, and
   Performance Guard: deployments that regress latency, queries, or errors are blocked.
4. **Recover reliably** — encrypted off-site Restic backups with scheduled *verified*
   restore tests and a live recovery-time estimate.

## Architecture

```
Browser / CDN
      │
    Nginx  ── full-page cache · coalescing · SWR · stale-if-error
      │
   PHP-FPM (per-site pools, auto-sized OPcache)
      │
   MariaDB (hardware-aware tuning) · optional Redis
─────────────────────────────────────────────────
Control plane
 ├── panel-api    unprivileged HTTP API + embedded React UI
 ├── panel-agent  privileged daemon, structured commands over Unix socket
 ├── SQLite       desired state (single source of truth)
 ├── Velocity Engine · Performance Guard · Restic backups · drift detection
```

The API never executes shell strings as root. It sends typed commands
(`CreateSite`, `DeployRelease`, `RestoreSnapshot`, …) to the agent over an
authenticated Unix socket. The agent renders configuration from desired state;
manual edits are detected as drift and surfaced for reconciliation, never
silently overwritten.

## Supported platform

Ubuntu 24.04 LTS, AMD64. One target, done well.

## Repository layout

```
cmd/panel-api     unprivileged API server (embeds ui/dist)
cmd/panel-agent   privileged agent daemon
cmd/slipctl       command-line client
internal/state    SQLite desired state, migrations, models
internal/rpc      API↔agent Unix-socket protocol
internal/engine   web-server abstraction + Nginx renderer (Velocity Engine)
internal/phpfpm   PHP-FPM pool renderer + OPcache sizing
internal/tune     hardware probe, MariaDB auto-tuner, worker calculators
internal/agent    provisioning, releases, staging, backups, certs, drift
internal/guard    Performance Guard regression gate
internal/api      HTTP handlers, auth, SSE
ui/               React + TypeScript + Vite panel UI
connector/        WordPress mu-plugin: precise purge + query metrics
installer/        install.sh + systemd units
bench/            k6 benchmark suite + Phase-0 playbook
docs/             architecture, security model, benchmarks
```

## Development

```
make build     # build all binaries (embeds the UI)
make test      # go vet + go test ./...
make ui        # build the React UI into ui/dist
make dist      # linux/amd64 release binaries
```

## License

Proprietary — © 2026 Slipstream. All rights reserved.
