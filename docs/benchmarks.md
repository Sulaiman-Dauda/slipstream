# Benchmarks

Performance is Slipstream's product claim, so this page gives the numbers, the method, and the
things we tried and **rejected** because they measured worse. The suite is in `bench/` and
`scripts/` — re-run it rather than believing this page.

## Method

The trap in any panel comparison is hardware. Two "identical" VPS instances of the same spec
measured **2.5× apart** on the same fixed workload, and a later pair differed by 16%. Any benchmark
that runs two panels on two machines is measuring the machines.

So: **both panels were benchmarked on the same physical server, one at a time**, with the OS
reinstalled in between. CPU parity was verified before and after with a fixed loop (0.51 s vs
0.50 s) and zero steal on both.

| | |
| --- | --- |
| Measured | 25–26 July 2026 |
| Versions | Slipstream 0.1.0 vs **CloudPanel CE 2.5.4** |
| Target | Vultr, 2 vCPU (Haswell), 1.9 GB RAM, Ubuntu 24.04.4 |
| Workload | WordPress 7.0.2, WooCommerce 10.9.4, TwentyTwentyFive, 102 posts, 50 products |
| Identical? | yes — same tarballs, SHA-256 verified on both installs |
| PHP / DB | 8.4 / MariaDB |
| Generator | `wrk` 4.1.0 |
| CloudPanel tuning | Varnish enabled with a hand-written WordPress/WooCommerce VCL, Redis object cache, OPcache JIT |
| Slipstream tuning | shipped defaults |

CloudPanel was tuned to its best deliberately: out of the box its Varnish caches nothing for
WordPress, because its stock VCL treats a response with no `Cache-Control` header as uncacheable and
WordPress does not send one. Comparing against an untuned competitor would have been dishonest.

Throughput is measured on loopback so both sides carry the same generator handicap; latency figures
are also taken over a real 97 ms WAN link.

These numbers describe **two specific versions on one specific day**, and CloudPanel is actively
developed — a later release may well close a gap shown here. That is why the version and date are
in the table above and the suite is in the repository: the comparison is meant to be re-run, not
cited indefinitely. If you re-run it and get different numbers, open an issue with the output and
this page gets corrected.

## Cached delivery

| Metric | Slipstream | CloudPanel | |
| --- | --- | --- | --- |
| Sustained, 500 connections, 60 s | **9,840 req/s** | 2,495 req/s | 3.9× |
| p99 at that load | **83 ms** | 6.46 s | 78× tighter |
| Single-connection latency (p50) | **139 µs** | 487 µs | 3.5× |
| 200-connection spike | **8,903 req/s** | 2,556 req/s | 3.5× |
| Static assets, 200 connections | **21,024 req/s** | 5,117 req/s | 4.1× |

## Resilience

| Metric | Slipstream | CloudPanel |
| --- | --- | --- |
| 2,000-connection flood | **8,051 req/s, 0 errors** | 163 req/s, 100 timeouts |
| Peak memory under the full suite | **823 MB, no swap** | 1,754 MB **+ 574 MB swap** |

The memory result is the interesting one. CloudPanel ships `pm.max_children = 250` regardless of the
machine; under load its FPM climbed to 64 workers at ~104 MB each and pushed the box into swap, which
is what produced the 13-second p50 on its dynamic test. Reaching its own configured limit would need
roughly 26 GB of RAM. Slipstream sizes workers from the memory actually available and never swapped.

## Footprint

| | Slipstream | CloudPanel |
| --- | --- | --- |
| Install time | **79 s** | 410 s |
| Disk | **+0.67 GB** | +4.44 GB |
| RAM, idle | **+93 MB** | +432 MB |
| Services added | **+8** | +23 |
| PHP versions installed | **1** | 10 |

## Where CloudPanel wins

Stated plainly, because a benchmark page that shows only wins is marketing.

**Uncacheable dynamic rendering.** A WooCommerce product page that cannot be cached renders in
~189 ms on Slipstream versus ~168 ms on CloudPanel. Shop-listing renders are level (273 ms vs
283 ms).

The cause is measured, not guessed: the `open_basedir` jail costs **72 ms of a 301 ms render (24%)**,
confirmed by removing it (301 ms → 229 ms) and putting it back (303 ms). CloudPanel sets no
`open_basedir` at all — it isolates tenants by Unix user alone. We keep the jail. A panel that is 12%
faster on uncached renders and lets one compromised site read another's files is not a trade worth
making.

Under a pure uncacheable flood both panels collapse to about 6 req/s on this hardware. Neither makes
an uncacheable WooCommerce store fast on a 2-core box; there the difference is noise.

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
# on the target
IP=<public-ip> PANEL_EMAIL=<email> PANEL_PW=<password> bash scripts/e2e-verify.sh

# then drive load from a second machine
wrk -t4 -c500 -d60s --latency -H 'Accept-Encoding: gzip' https://your-site/shop/
```

If you re-run this, check CPU parity first and report the hardware. A benchmark without that is a
hardware review.
