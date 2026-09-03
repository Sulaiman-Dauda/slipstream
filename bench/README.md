# Benchmark playbook: Slipstream vs CloudPanel

The numbers in [docs/benchmarks.md](../docs/benchmarks.md) came from this
playbook. Re-run it rather than citing that page indefinitely: it describes two
specific versions on one specific day, and CloudPanel is actively developed.

## The one rule that matters

**Both panels run on the same box, one at a time, with the OS reinstalled in
between.**

Not two machines, and never at the same time. Two "identical" VPS instances of
the same spec measured **2.5x apart** on a fixed CPU loop, and a later pair
differed by 16%. That is larger than most of the effects being measured, so a
benchmark spread across two machines is measuring the machines. Running both
panels on one box simultaneously is worse: they contend for CPU, memory and
disk, and both want port 443, nginx and MariaDB.

`bench/cpu-parity.sh` is the check. Run it before and after each stack and
record the output. If the loop time moves between runs, or steal is ever
non-zero, the box moved and the comparison is void whatever the throughput said.

Run **A/B/A**: Slipstream, then CloudPanel, then Slipstream again. If the two
Slipstream runs disagree beyond noise, something drifted and you would otherwise
never know. It costs one extra run.

## Setup

Identical on both stacks, or the comparison is not about the panels:

- same WordPress, WooCommerce, theme and imported dataset, from the same
  tarballs, SHA-256 verified on each install
- same PHP version and database engine
- **CloudPanel tuned to its best**, not to its defaults. Out of the box its
  Varnish caches nothing for WordPress, because its stock VCL treats a response
  with no `Cache-Control` header as uncacheable and WordPress sends none.
  Publishing a win over an untuned competitor would be dishonest. The July run
  gave it a hand-written WordPress/WooCommerce VCL, Redis object cache and
  OPcache JIT.
- Slipstream on shipped defaults
- load generated from a **second machine in the same region**, never on the
  target

## Run

```sh
# on the target, before and after each stack
bench/cpu-parity.sh

# from the generator, once per stack
bench/wrk-suite.sh https://bench.example.com slipstream-v0.2.0
bench/wrk-suite.sh https://bench.example.com cloudpanel-2.6.0
```

`wrk-suite.sh` runs every scenario behind the published table (sustained cached
at 500 connections, single-connection latency, a 200-connection spike, static
assets, a 2,000-connection flood, and an uncacheable render through
`wrk/uncached.lua`) and writes `bench/results/<label>/summary.tsv`, which diffs
cleanly between runs.

Three runs per stack, alternating; discard the first as disk-cache warmup.

## Also record

Numbers the load generator cannot see, taken on the target:

| Metric | How |
| --- | --- |
| Install time | time the installer end to end, from a fresh OS |
| Disk and RAM added | measure before install and at idle after |
| Peak memory and swap | sample `free -m` through the suite; swap is the interesting one |
| Services and PHP versions added | `systemctl list-units --type=service`, `ls /etc/php` |
| Database load | `SHOW GLOBAL STATUS LIKE 'Questions'` deltas |
| Object-cache flush propagation | change an option with wp-cli, time until the web tier serves it |

## Publish

Every number with the versions, the date, the exact hardware and the configs
that produced it, and the rows where Slipstream loses alongside the rows where
it wins. Claim wording is *"faster under the published WordPress benchmark and
configuration"*, never "always faster".

If you re-run this and get different numbers, open an issue with the output and
docs/benchmarks.md gets corrected.
