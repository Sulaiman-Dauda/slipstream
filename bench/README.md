# Phase-0 benchmark playbook: Slipstream vs CloudPanel

The performance claim must be earned before it is marketed. This playbook
produces a reproducible, publishable comparison.

## Rules

- Identical VMs (same provider, region, type, disk), created the same day.
- Identical WordPress: same version, theme (Twenty Twenty-Four), plugin set
  (WooCommerce + sample products on the commerce run), same imported dataset.
- Same PHP version on both.
- Load generator runs on a third machine in the same region (never on the
  target).
- Every number published with the exact configs used to produce it.
- Claim wording: *"faster under the published WordPress benchmark and
  configuration"* — never "always faster".

## Machines

| Role | Example |
|---|---|
| A: CloudPanel | Hetzner CCX23 (4 vCPU / 16 GB), Ubuntu 24.04 |
| B: Slipstream | identical |
| C: load generator | CX32, k6 installed |

## Setup

1. **A**: install CloudPanel per its docs, create a WordPress site with its
   recommended settings (Nginx + PHP-FPM + MySQL, its default caching).
2. **B**: `curl -fsSL https://get.slipstream.example | sudo bash`, create a
   WordPress site, Balanced profile (Commerce for the WooCommerce run).
3. Import the same content dump into both (~500 posts, media library).
4. Point `bench-a.example.com` / `bench-b.example.com`, issue certificates.
5. Warm both: `for i in $(seq 200); do curl -s https://…/ >/dev/null; done`

## Run

From machine C, against each target:

```
k6 run -e TARGET=https://bench-a.example.com bench/k6/wordpress.js | tee results-a.txt
k6 run -e TARGET=https://bench-b.example.com bench/k6/wordpress.js | tee results-b.txt
```

Three runs each, alternating order; discard the first (disk cache warmup),
publish mean ± stddev of the rest.

## Record

| Metric | Where |
|---|---|
| Cached RPS + p50/p95/p99 | k6 `cached` scenario |
| Uncached RPS + p50/p95/p99 | k6 `uncached` scenario |
| Error rate under 10x spike | k6 `spike` scenario |
| Cache hit ratio | `slipstream_cache_hit` (Slipstream) / header inspection (CloudPanel) |
| CPU + memory during each phase | `vmstat 1` on the target |
| MariaDB queries/sec | `SHOW GLOBAL STATUS LIKE 'Questions'` deltas |

## What we expect to win on (and why)

- **Spike scenario**: request coalescing means one PHP regeneration per URL
  while CloudPanel's default config lets the burst through to PHP-FPM.
- **Stale-while-revalidate**: zero slow requests at TTL expiry; the default
  CloudPanel site pays full regeneration latency.
- **Uncached path**: calculated OPcache + InnoDB buffer pool sizing vs
  static defaults on small instances.

If a scenario does NOT show a win, that result gets published too, and the
default configuration gets fixed until it does.
