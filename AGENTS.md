# Slipstream — agent brief

Performance-first hosting control plane: one command to install, one click to launch a
production-ready site, with caching, tuning, isolation and off-site backups with verified
restores. Go, `go 1.25`, module `github.com/slipstream-panel/slipstream`. See `README.md`.

## Status: RELEASE CANDIDATE

Not yet carrying production traffic — unlike its sibling
[Windlass](../deploy-panel/AGENTS.md), which is live. That still makes this the safer of the two
panels to change, but the two overlap conceptually, so check whether a fix here also applies
there (and vice versa) rather than letting them drift.

Verified on **Ubuntu 24.04 (PHP 8.4, nginx 1.24) and 26.04 (PHP 8.5, nginx 1.28)**: clean install
from bare OS, `scripts/e2e-verify.sh` 43/43 on both, and a head-to-head benchmark against
CloudPanel on identical hardware. HTTP/3 is capability-gated — it turns itself on where nginx has
`ngx_http_v3_module` (26.04) and stays off where it does not (24.04); verified with a real QUIC
client, not just config inspection.

**Before believing a performance or config claim, measure it on a box.** This project has a long
run of bugs that looked correct in the diff and were wrong on the wire — security headers silently
dropped by nginx's `add_header` inheritance rule, an HTTP/3 probe reading stdout for a tool that
writes to stderr, a "WooCommerce" site type that never installed WooCommerce. The e2e gate exists
because of those; extend it rather than trusting a code read.

## Run it

```sh
make build     # binary
make ui        # frontend
make dist      # distributable
make vet
```

Layout: `cmd/` entrypoints, `internal/` core logic, `ui/` frontend, `installer/` the install
path, `connector/` remote agent, `bench/` performance benchmarks, `scripts/` tooling.

## Tests

```sh
make test
make vet
```

`bench/` exists because performance is the product claim. If you change caching, request
handling or the tuning defaults, run the benchmarks and report the before/after numbers — a
performance panel that quietly gets slower is worse than useless.

## Hard rules

1. **Never run the installer or connector against a real server** you care about. It is designed
   to take over system configuration — use a throwaway VM or container.
2. **Never point a local run at a production host or its database.**
3. **Stage and show the diff; get an explicit go before committing or pushing.**
4. Don't weaken the backup/restore verification path to make a test pass. "Verified restores" is
   a core claim; a backup that isn't verified doesn't count.

## Gotchas

- Two panels with overlapping scope live in this folder. Slipstream is hosting-and-performance
  focused; Windlass is Compose-deployment focused. Don't merge concepts between them casually.
