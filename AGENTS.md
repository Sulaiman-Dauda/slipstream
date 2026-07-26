# Slipstream — agent brief

Performance-first hosting control plane: one command to install, one click to launch a
production-ready site, with caching, tuning, isolation and off-site backups with verified
restores. Go, `go 1.25`, module `github.com/slipstream-panel/slipstream`. See `README.md` for the
public description and `docs/` for the full documentation.

## Status: release candidate — safe to run, not yet battle-tested

Read this honestly before making claims in a commit message, an issue or the docs:

- **Works, end to end, on both supported releases.** Clean install from bare OS on
  **Ubuntu 24.04** (PHP 8.4, nginx 1.24) and **26.04** (PHP 8.5, nginx 1.28);
  `scripts/e2e-verify.sh` **43/43 on both**; benchmarked head-to-head against CloudPanel on
  identical hardware.
- **Not yet carrying real production traffic.** No third-party has run it, and it has had no
  external security review. Its sibling [Windlass](../deploy-panel/AGENTS.md) is the one that is
  live.
- **The defect discovery rate is still high.** 21 real bugs were found in roughly two weeks of
  deliberate pressure-testing, including an authorization bypass, a site-wipe path and a
  SQL-console bypass. That number is going down, but it is not yet low enough to describe this as
  "production proven". Say "release candidate", not "production ready".

**Recommendation for anyone picking this up:** run it on servers you can afford to rebuild, take
backups off-box, and give the privileged surface (auth, RPC, file manager, SQL console, privilege
boundaries) a dedicated security review before it fronts anything that matters.

## The one habit that matters here

**Measure it on a box. Do not trust the diff.** Nearly every bug in this project's history looked
correct in review and was wrong on the wire:

- security headers silently dropped, because nginx inherits `add_header` *only* when the inner
  block declares none — they were absent from every real response for months;
- an HTTP/3 capability probe reading stdout from `nginx -V`, which writes to stderr, so the
  feature never activated anywhere;
- a `woocommerce` site type that never installed WooCommerce;
- kernel TLS, copied from a competitor as an obvious win, which cost **28%** of cached throughput.

Check the response headers, the negotiated protocol, the listening socket, the syscalls. Extend
`scripts/e2e-verify.sh` rather than relying on a code read — and when the gate passes, confirm it
is asserting on the product's behaviour and not on the test's own setup. That mistake has been
made here twice.

## Run it

```sh
make build     # binaries into dist/
make ui        # frontend
make dist      # distributable
make test
make vet
```

Layout: `cmd/` entrypoints, `internal/` core logic, `ui/` frontend, `installer/` the install
path, `connector/` the WordPress mu-plugin, `bench/` performance benchmarks, `scripts/` tooling,
`docs/` user-facing documentation.

Verify a real server end to end:

```sh
IP=<public-ip> PANEL_EMAIL=<email> PANEL_PW=<password> bash scripts/e2e-verify.sh
```

## Hard rules

1. **Never run the installer or connector against a real server** you care about. It is designed
   to take over system configuration — use a throwaway VM or container.
2. **Never point a local run at a production host or its database.**
3. **Stage and show the diff; get an explicit go before committing or pushing.**
4. Don't weaken the backup/restore verification path to make a test pass. "Verified restores" is
   a core claim; a backup that isn't verified doesn't count.
5. `bench/` exists because performance is the product claim. If you change caching, request
   handling or the tuning defaults, run the benchmarks and report before/after numbers — a
   performance panel that quietly gets slower is worse than useless.
6. Performance defaults are **A/B measured, one directive at a time**, and anything that does not
   reproduce a win is not shipped. `installer/install.sh` records what was tried and rejected,
   with numbers; add to that list rather than quietly re-adding a setting.

## Gotchas

- Two panels with overlapping scope live in this folder. Slipstream is hosting-and-performance
  focused; Windlass is Compose-deployment focused. Don't merge concepts between them casually.
- `internal/agent/connector.go` embeds the canonical PHP from
  `connector/slipstream-connector/`. Edit the PHP, then regenerate — `TestConnectorCopiesMatch`
  fails if they drift.
- wp-cli and PHP-FPM have **separate APCu segments**. A CLI cache flush cannot clear FPM's, and
  an FPM *reload* (SIGUSR2) keeps the master alive and APCu with it. Use
  `Agent.refreshSiteState`, which asks the site's own connector to flush through its FPM pool.
- Detect an installed PHP version by its **FPM binary** (`/usr/sbin/php-fpm<ver>`), never by
  `/etc/php/<ver>/` — rendering a pool creates that directory, so a single failed provision
  leaves one behind that reads as "installed" forever.
