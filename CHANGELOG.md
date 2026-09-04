# Changelog

All notable changes to Slipstream are recorded here. This project follows
[semantic versioning](https://semver.org) once it reaches 1.0.

## Unreleased

### Added

- `panel-api recover-admin`, a root-side way back into the panel. Losing the password meant
  losing the panel: there was no reset, no way to create an account from the host, and the only
  password endpoint needed a session you could not get. Root could read the database holding the
  accounts and could not do anything with it.

  Physical access to the server is the credential, which is the same trust boundary every other
  recovery mechanism uses. It works whether the panel is running, stopped or never finished
  setup, because it opens the state database directly.

  The password is generated rather than accepted as an argument, so it never reaches the shell
  history or `ps`. A reset revokes that account's sessions. With several accounts it lists them
  rather than guessing which to reset. `--disable-2fa` is separate and opt-in, for a lost device.
  Every use leaves an audit event.


## [Unreleased]

## [0.2.2] — 2026-09-04

### Added

- **The panel tells you when an update exists.** A banner on the dashboard names the newer version,
  links to what changed, and offers an Update now button. Nothing installs by itself: the check
  reports, the operator decides. Before this there was no way to learn a new release existed, and
  the only control was a button labelled "Check & update" that checked nothing and updated
  immediately.
- **The changelog is published at [slipstreampanel.com/changelog](https://slipstreampanel.com/changelog)**,
  generated from this file, so every release ships its own notes and the panel can link to them.
- **Two-factor authentication can be required on admin accounts** (`require_2fa_admin`, off by
  default). An admin can do anything root can do on the machine, so a second factor should be
  enforceable rather than per-account opt-in. While it is on, an admin without one can reach only
  their own account and the enrolment routes, so turning it on cannot lock anyone out.
- **The panel can be restricted to known addresses.** Its vhost now includes
  `/etc/slipstream/panel-access.conf` if present, so `allow`/`deny` rules survive the certificate
  operations that regenerate that file. A firewall rule cannot do this, because the sites share
  port 443.

### Fixed

- **The settings page could not save.** `GET /api/settings` returns the read-only `panel_domain`
  alongside the editable keys, the page sent the whole object back, and the handler rejected it, so
  Save answered `400 panel_domain is set by the operation that owns it` on a form nobody had
  edited. Every settings change through the UI has failed since that field became readable.
- **The update button had no source.** It posted an override that was never saved (`update_url` is
  deliberately not writable: it is where root binaries come from) and got "no update URL
  configured". It now defaults to the project's own releases, and the field that could never work
  is gone.

### Security

- **Updates verify build provenance, and fail closed.** The checksums are published in the same
  release as the binaries, so anyone able to write a release could replace both and reach root on
  every server that updates. GitHub's signed attestation proves the bytes came from this project's
  release workflow at a specific commit and cannot be forged by rewriting the release. A check that
  runs and fails aborts the update always; a host with no `gh` to check with is refused unless the
  caller explicitly accepts that, which is recorded in the audit log.

## [0.2.1] — 2026-09-03

### Fixed

- **A deploy silently turned the object cache off.** The drop-in is a symlink into `shared/`,
  written once at provisioning, and nothing re-linked it into a new release directory, so the
  first deploy after provisioning left the site with no object cache while the panel went on
  reporting `object_cache: true`. Found on a live server where a site had been in that state
  for two weeks; `wp cache flush` even reports success, because there is nothing to flush. A
  deploy now makes the release agree with the site's recorded setting, in both directions, so
  installs that already lost their drop-in repair themselves on the next deploy.
- **Enabling the object cache started a Redis daemon that nothing used.** The endpoint called
  `ensureRedis()` whenever it was asked to enable caching, but the request carries no backend
  and the agent only selects Redis when explicitly told to, so every call installed the APCu
  drop-in and the daemon sat there unused. On the 1 GB servers supported since 0.2.0 that is
  real memory spent on nothing, and the progress message described work that was not happening.

### Changed

- The benchmark suite behind `docs/benchmarks.md` is now **in the repository** rather than
  described by it: `bench/wrk-suite.sh` runs every published scenario, `bench/cpu-parity.sh`
  is the hardware check the method depends on, and `bench/README.md` documents the one-box
  A/B/A method actually used. The figures were re-measured against CloudPanel CE 2.5.4 on
  3 September 2026 and now report the timeout count beside each percentile, because `wrk`
  drops timed-out requests from the latency distribution and a panel that fails to answer
  otherwise posts a better p99 than one that answered slowly.
- The end-to-end suite is **55 checks**, verified green on both Ubuntu 24.04 and 26.04. Its
  migration fixture had been writing a database dump into a directory the site user cannot
  write to, with stderr discarded, so three checks failed for one invisible reason.

## [0.2.0] — 2026-09-03

### Added

- **`panel-api recover-admin`.** Losing the panel password meant losing the panel: no reset,
  and the only password endpoint needed a session you could not get. Run from the host, it
  lists accounts, resets one, creates the first admin when none exists, and clears 2FA only
  when asked. The password is generated and printed once rather than accepted as an argument,
  a reset revokes that account's sessions, and every use writes an audit event.
- **1 GB servers are supported.** Preflight refused anything under 1500 MB and the docs asked
  for 2 GB, which undersold the lighter stack the README compares against CloudPanel.

### Fixed

- **The object cache drop-in installed itself over a segment it could not reach.** The guard at
  the top of `object-cache.php` was a bare `return` above the function and class declarations,
  and PHP hoists those, so it never prevented anything. Under `apc.enable_cli=0` every wp-cli
  cache call returned false, `wp cache flush` died, and on a server with no APCu extension the
  first cache write fatalled the site. When APCu is unusable the drop-in now behaves like
  WordPress's own request-scoped cache.
- **A flush from wp-cli did not reach PHP-FPM.** The two hold separate APCu segments, so a theme
  activated from the command line kept serving the old one until PHP-FPM restarted. Keys now
  carry an epoch from a file both processes read, and a flush writes a new one.
- **A deploy dropped the cache connector, and drift could not see it.** `installConnector` ran at
  provisioning and on import but never on `DeployRelease`, so a build artefact from CI promoted a
  release with no mu-plugin in it.
- **An imported site dropped the cache connector**, for the same reason: the archive carries the
  source host's `wp-content`, which has no connector in it.
- **Block theme edits served stale pages.** Changing a header, footer, template, global style or
  navigation in the Site Editor left every interior page on the old version until its TTL expired,
  up to 24 hours on the Maximum profile. Those changes now purge the whole site.
- **Guard measured the box rather than the change**, which undercut the promise a promotion is
  gated on. The site budget is now divided per site and migration is covered.
- **Password changes and the panel domain gave no feedback.** The panel did the work and did not
  say so, so a saved domain looked like it had failed.
- **The panel silently fell back to a system font.** Vite inlined a 2,028 byte JetBrains Mono
  subset as a `data:` URI, under its 4 kB limit, and the API's own `font-src 'self'` rejected it.
- CSS side-effect imports now typecheck, via Vite's client types.

### Changed

- ShellCheck comes from the runner instead of apt. One CI run sat in `apt-get install` for over
  two hours on 19/08 and had to be cancelled by hand, blocking a dependency PR that was green.
- Dependencies: React 19.2.8, react-router-dom 7, TypeScript 7, Vite 8, `@vitejs/plugin-react` 6,
  `golang.org/x/crypto` 0.55.0, `modernc.org/sqlite` 1.56.0.

## [0.1.2] — 2026-07-26

### Added
- **Build provenance attestations.** Every released binary now carries a
  GitHub-signed statement proving it was built by this repository's release
  workflow at a specific commit. Verify with
  `gh attestation verify panel-agent --repo Sulaiman-Dauda/slipstream`; the
  installer checks it automatically when the GitHub CLI is present.
- CodeQL analysis for Go and TypeScript.
- `ROADMAP.md`, and a written review standard in `.github/REVIEW_GUIDELINES.md`.

### Changed
- All GitHub Actions pinned to immutable commit SHAs.
- Every workflow declares explicit `permissions`.
- Dependency updates grouped by minor/patch, with majors kept separate.

### Fixed
- The documentation-site rebuild required a personal access token that was never
  configured, so it failed on every docs change.

## [0.1.1] — 2026-07-26

### Added
- Installer progress display: a spinner per phase, a tick with its measured
  duration, and a full log at `/var/log/slipstream-install.log`. Degrades to
  plain lines when not attached to a terminal.
- `https://get.slipstreampanel.com` as the install URL, with `/vX.Y.Z` to pin.

### Fixed
- Installer output no longer interleaves apt, systemctl and nginx messages with
  the progress display; a failure now prints the tail of the log.

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
