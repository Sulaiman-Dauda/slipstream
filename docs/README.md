# Documentation

These pages are published at **[slipstreampanel.com](https://slipstreampanel.com)**.

Slipstream is a self-hosted hosting control panel focused on performance: sites arrive cached,
tuned and isolated, and a deployment that makes a site slower is refused rather than shipped.

This folder is the authoritative documentation. It is plain markdown so it reads well on GitHub,
and a future project website will build its guides from these same files.

## Where to start

- **[Getting started](./getting-started.md)** — install on a fresh server, create the first site,
  make the first deployment.
- **[Concepts](./concepts.md)** — sites, releases, profiles and the desired-state model. Read this
  once and the rest of the panel makes sense.
- **[Sites](./sites.md)** — every site type, what it provisions, and the per-site options.

## Day to day

- **[Caching](./caching.md)** — what the Velocity Engine caches, what it never caches, and how
  invalidation works.
- **[Staging and Safe Push](./staging-and-safe-push.md)** — clone production, change it safely,
  let Performance Guard decide, roll back in one command.
- **[Backups](./backups.md)** — Restic repositories, schedules, and why a backup does not count
  until it has been restored.
- **[Operations](./operations.md)** — upgrades, logs, drift, firewall, monitoring.

## Reference

- **[CLI reference](./cli.md)** — `slipctl`.
- **[API reference](./api.md)** — every HTTP endpoint and the role it requires.
- **[Architecture](./architecture.md)** — how the two processes, the state database and the data
  plane fit together.
- **[Security model](./security.md)** — the trust boundaries, what is jailed, and what is
  explicitly out of scope.
- **[Benchmarks](./benchmarks.md)** — the numbers, the method, and the tuning we rejected.

## When something is wrong

- **[Troubleshooting](./troubleshooting.md)** — symptoms, causes and fixes.
- **[FAQ](./faq.md)** — common questions.

## What Slipstream is, in one paragraph

You give Slipstream a fresh Ubuntu server. It installs and configures nginx, PHP-FPM and MariaDB,
then hands you a web panel. You add a site by choosing a type and a domain; Slipstream creates a
dedicated Unix user, PHP-FPM pool, database and jail for it, installs the application, renders the
web-server configuration with full-page caching already switched on, and obtains a certificate.
Code lives in immutable releases with a `current` symlink, so promoting and rolling back are
atomic. Before a change reaches production you can clone the site to staging and have Performance
Guard compare the two — a candidate that regresses p95, query count or error rate is blocked.
Backups go off-site encrypted, and are periodically restored to prove they work.

## Project links

- Source code and issues: <https://github.com/Sulaiman-Dauda/slipstream>
- [Contributing guide](../CONTRIBUTING.md)
- [Security policy](../SECURITY.md)
- [Licence (AGPL-3.0)](../LICENSE)
