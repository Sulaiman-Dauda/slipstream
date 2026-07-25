# Slipstream — agent brief

Performance-first hosting control plane: one command to install, one click to launch a
production-ready site, with caching, tuning, isolation and off-site backups with verified
restores. Go, `go 1.25`, module `github.com/slipstream-panel/slipstream`. See `README.md`.

## Status: IN TESTING

Not yet in production use — unlike its sibling [Windlass](../deploy-panel/AGENTS.md), which is
live. That makes this the safer of the two panels to change, but the two overlap conceptually, so
check whether a fix here also applies there (and vice versa) rather than letting them drift.

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
