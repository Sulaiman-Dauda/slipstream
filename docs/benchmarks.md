# Benchmarks

Performance is Slipstream's product claim, so this page gives the numbers, the method, and the
things we tried and **rejected** because they measured worse. The suite is in `bench/` and
`scripts/` — re-run it rather than believing this page.

## Method

The trap in any panel comparison is hardware. Two "identical" VPS instances of the same spec
measured **2.5× apart** on the same fixed workload, and a later pair differed by 16%. Any benchmark
that runs two panels on two machines is measuring the machines.

So: **both panels were benchmarked on the same server, one at a time**, with the OS reinstalled in
between. The run order was **Slipstream, CloudPanel, Slipstream again**, because a comparison that
cannot detect its own box drifting is not evidence. CPU parity was checked with a fixed loop before
and after every stack: 2.20s and 2.13s, then 2.20s and 2.21s, then 2.24s and 2.23s, with zero steal
throughout. The two Slipstream runs agree within 5% on every scenario, against differences of
4× to 18× between the panels.

| | |
| --- | --- |
| Measured | 3 September 2026 |
| Versions | Slipstream **v0.2.0** vs **CloudPanel CE 2.5.4** (its current release, dated 1 July 2026) |
| Target | Vultr, 2 vCPU (Haswell), 1.9 GB RAM, Ubuntu 24.04.4 |
| Workload | WordPress 7.1, WooCommerce 11.1, TwentyTwentyFive, 102 posts, 50 products |
| Identical? | yes, the same database dump and `wp-content` archive imported into both, SHA-256 verified |
| PHP / DB | 8.4 / MariaDB 10.11 on both |
| Generator | `wrk` 4.1.0 |
| CloudPanel tuning | Varnish put in front with a hand-written WordPress/WooCommerce VCL, Redis object cache, OPcache JIT |
| Slipstream tuning | shipped defaults |
| Suite | `bench/wrk-suite.sh`, `bench/cpu-parity.sh` |

CloudPanel was tuned to its best deliberately. Out of the box its Varnish caches nothing for
WordPress: the stock VCL sets `beresp.ttl = 0s` for any response without a `Cache-Control` header
and WordPress sends none, and CloudPanel does not route a new site through Varnish at all. It was
put in the path, given rules that cache anonymous pages while bypassing cart, checkout, account and
any session carrying a WooCommerce cookie, and verified serving `x-cache: HIT` on the shop listing
with `/cart/` still bypassing. Comparing against an untuned competitor would have been dishonest.

Throughput is measured on **loopback**, so both panels carry the same generator handicap. That
handicap is real and now measured: driving the same target from a separate 4 vCPU machine gives
Slipstream 11,986 req/s against 9,280 on loopback, and CloudPanel 2,332 against 2,259. Loopback
figures therefore understate both, fairly. Both sets are below.

These numbers describe **two specific versions on one specific day**. CloudPanel is actively
developed and a later release may close a gap shown here. The suite is in the repository so the
comparison can be re-run rather than cited indefinitely. If you re-run it and get different
numbers, open an issue with the output and this page gets corrected.

## Cached delivery

Loopback, and the timeout count belongs beside the percentile: `wrk` drops a request that exceeds
its timeout from the latency distribution, so a panel that fails to answer can post a flattering
p99. Both columns come from the same runs.

| Metric | Slipstream | CloudPanel | |
| --- | --- | --- | --- |
| Sustained, 500 connections, 60 s | **9,280 req/s** | 2,259 req/s | 4.1× |
| p99 at that load | **85.5 ms**, 1 request timed out | 385.9 ms, **319 timed out** | |
| Requests completed in that minute | **570,864** | 135,661 | 4.2× |
| Single-connection latency (p50) | **146 µs** | 493 µs | 3.4× |
| 200-connection spike | **9,941 req/s** | 2,598 req/s | 3.8× |
| Static asset, 1 KB, 200 connections | **12,636 req/s** | 2,767 req/s | 4.6× |

Slipstream figures are the mean of the two runs; the spread between them was under 5%.

## Over a network rather than loopback

Driven from a separate 4 vCPU machine in the same region, which is closer to what a visitor
experiences and removes the generator from the target's cores.

| Metric | Slipstream | CloudPanel | |
| --- | --- | --- | --- |
| Sustained, 500 connections | **11,986 req/s** | 2,332 req/s | 5.1× |
| Static asset, 1 KB | **16,620 req/s** | 2,936 req/s | 5.7× |
| 2,000-connection flood | **11,483 req/s** | 1,562 req/s | 7.4× |

## Resilience

| Metric | Slipstream | CloudPanel |
| --- | --- | --- |
| 2,000-connection flood | **8,018 req/s, no errors** | 455 req/s, 1,619 timeouts and 80 read errors |
| Peak memory under the full suite | **880 MB, no swap** | 1,315 MB, and it swapped |

CloudPanel ships `pm.max_children = 250` regardless of the machine, and under load its FPM pool
grows until the box swaps. Slipstream sizes workers from the memory actually available.

## Footprint

| | Slipstream | CloudPanel |
| --- | --- | --- |
| Install time | **105 s** | 460 s |
| Disk | **+0.61 GB** | +4.58 GB |
| RAM, idle | **+151 MB** | +498 MB |
| Services added | **+8** | +24 |
| PHP versions installed | **1** | 10 |

## Where CloudPanel wins

Stated plainly, because a benchmark page that shows only wins is marketing.

**Uncacheable dynamic rendering**, and by more than the last run showed.

| Metric | Slipstream | CloudPanel | |
| --- | --- | --- | --- |
| Uncacheable shop listing, loopback | 5.48 req/s | **10.75 req/s** | 2.0× |
| The same over the network | 5.44 req/s | **21.99 req/s** | 4.0× |

The gap widens once the generator is off the target's cores, which is the signature of a CPU-bound
path: on loopback both panels are competing with the load generator for two cores, and that
compression flatters the slower one.

The cause is measured, not guessed: the `open_basedir` jail costs **72 ms of a 301 ms render (24%)**,
confirmed by removing it (301 ms to 229 ms) and putting it back (303 ms). CloudPanel sets no
`open_basedir` at all, isolating tenants by Unix user alone. We keep the jail. A panel that renders
uncached pages faster and lets one compromised site read another's files is not a trade worth
making, and this is the row where that choice shows up in a number.

Neither panel makes an uncacheable WooCommerce store fast on a 2-core box. Both are in single or
low double digits of requests per second, where the practical answer is to cache the page, which is
the rest of this document.

## What we tried and rejected

Every tuning candidate was A/B measured on the same box, one directive at a time. These did not
survive:

| Change | Result | Verdict |
| --- | --- | --- |
| **Kernel TLS** (`ssl_conf_command Options KTLS`) | sustained 9,177 → **6,642** req/s, static 19,601 → **14,246** | **Rejected.** CloudPanel ships it. We implemented it, confirmed the kernel really was encrypting, and measured a 28% loss on cached throughput and 31% on static. KTLS pays off on large sequential transfers; a page cache sends many small responses, where per-record kernel transitions cost more than the copy they save. |
| **`multi_accept on`** | 9,529 vs 9,438 req/s across repeats — overlapping | **Rejected.** No reproducible difference. Config we cannot justify does not ship. |
| **Flattening the docroot** (depth 6 → 4, to shrink the `open_basedir` stat storm) | would recover ~8% of the dynamic path only | **Declined.** The whole jail costs 72 ms; removing two of six path components recovers a fraction of that, in exchange for surgery on release symlinks, SFTP chroot, backups, staging and migration. Bad trade. |
| **More PHP workers** | 6 workers → 5.54 req/s, 10 → 4.29, 16 → 4.24, with 0% CPU idle throughout | **Rejected.** The uncacheable path is CPU-bound, not worker-starved. More workers only add context switching and memory pressure. Our conservative default is not merely safe, it is optimal here. |

## What we adopted

| Change | Effect |
| --- | --- |
| `worker_connections` 768 → 4096, `worker_rlimit_nofile` 65535 | two workers at 768 capped the box at ~1,536 connections — exactly where a 2,000-connection flood produced 7,421 connect errors. Now **0 errors and +37% throughput** |
| `open_file_cache` | static **+28%** (16,375 → 21,024 req/s); WAN static p99 512 ms → **97.6 ms** |
| Kernel tuning (somaxconn, backlogs, `tcp_tw_reuse`, no slow-start-after-idle) | the accept queue was overflowing under burst before any worker was busy |
| FPM `emergency_restart`, `listen.backlog`, `rlimit_files` | a pool poisoned by crashing workers restarts itself instead of serving 502s |
| **Pre-compressed page cache** | the single biggest win in the project's history: cached WooCommerce throughput 944 → 8,664 req/s, because a cache hit no longer pays for gzip |

## Reproducing this

```bash
# on the target, before and after each stack
bench/cpu-parity.sh

# every scenario above, in one run, from the target (loopback) or a bigger
# machine in the same region (network). See bench/README.md for the full method.
bench/wrk-suite.sh https://your-site slipstream-v0.2.0

# the panel's own end-to-end suite, 55 checks
IP=<public-ip> PANEL_EMAIL=<email> PANEL_PW=<password> bash scripts/e2e-verify.sh
```

If you re-run this, check CPU parity first and report the hardware. A benchmark without that is a
hardware review.
