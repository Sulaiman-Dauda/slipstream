# Changelog

All notable changes to Slipstream are recorded here. This project follows
[semantic versioning](https://semver.org) once it reaches 1.0.

## [Unreleased]

## [0.1.0] — 2026-07-26

First tagged release. Everything below was built and verified before this tag; the
version number simply makes it installable with the one-line installer, which
fetches binaries and their SHA-256 checksums from the release assets.

Pre-1.0. Slipstream is a release candidate: it works end to end on both supported Ubuntu releases,
but has not yet carried production traffic and has had no external security audit.

### Added

- **Ubuntu 26.04 LTS support** alongside 24.04, with PHP 8.5, nginx 1.28 and MariaDB 11.8. Both
  releases are verified on every change.
- **HTTP/3 (QUIC)**, enabled automatically where the OS ships an nginx built with
  `ngx_http_v3_module` and left off where it does not. Verified with a real QUIC client, not just by
  reading the config.
- **Log rotation** for per-site nginx and PHP logs — daily, 14 days, compressed. Without it a busy
  site fills the disk and takes every site on the box down.
- **Performance tuning** taken from a head-to-head benchmark: larger nginx worker connections and FD
  limits, kernel accept-queue tuning, `open_file_cache`, and FPM self-heal (`emergency_restart`),
  backlog and FD limits.
- `--version` and `--help` on all three binaries.
- Full documentation set under `docs/`.

### Fixed

- **The `woocommerce` site type never installed WooCommerce.** It provisioned plain WordPress and set
  a WooCommerce-specific constant for a plugin that was not there, leaving `/shop`, `/cart` and
  `/checkout` returning 404.
- **A fresh install left nginx not listening on 443**, so the setup URL the installer printed could
  not be reached.
- **Security headers were absent from every real response.** nginx inherits `add_header` only when
  the inner block declares none, and both the PHP and static locations declared their own — so
  `X-Content-Type-Options`, `X-Frame-Options` and `Referrer-Policy` were silently dropped sitewide.
- **Stale object cache after out-of-band changes.** Restores, updates and migrations left the live
  site serving pre-change options and rewrite rules, because an FPM reload does not clear APCu. The
  panel now flushes the affected site's cache through its own pool.
- `/tmp` removed from every pool's `open_basedir`; it is shared between tenants and was not needed.
- Requesting an uninstalled PHP version now fails immediately with the available versions, instead of
  failing deep in provisioning and leaving a half-created site.
- Deleting a site now removes its log directory.

### Security

- **Cross-tenant database access (high).** The SQL console ran as the MariaDB superuser with the
  site's database only as the default schema, so an operator scoped to one site could read and write
  every other tenant's database by qualifying the table name. Queries now run as a throwaway account
  granted on exactly one database.
- **Login source addresses were never recorded.** Every login was logged as `127.0.0.1` because the
  API sits behind the panel's own nginx, defeating the audit trail and collapsing rate limiting onto
  one shared bucket. Forwarded headers are now trusted from loopback only.
- **File-manager symlink race.** A path was validated and then opened separately, as root, in
  directories the tenant owns. Reads and writes now use `openat2(RESOLVE_NO_SYMLINKS)` and ownership
  is applied through the descriptor.
- **TOTP replay.** Codes were valid for the whole ±1 step window with no record of use; the accepted
  step is now recorded per user.

### Changed

- Licence is **AGPL-3.0**.
- `pm.max_children` remains sized from available memory. Measured on a 2-core box, raising it makes
  uncacheable throughput *worse* (6 workers 5.54 req/s, 10 → 4.29, 16 → 4.24, 0% CPU idle throughout).
- Kernel TLS was implemented, measured at a 28% loss on cached throughput, and **removed**. The
  reasoning and numbers are kept in `installer/install.sh` so it is not re-added.
