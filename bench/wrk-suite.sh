#!/bin/sh
# Every scenario behind the table in docs/benchmarks.md, in one run.
#
#   bench/wrk-suite.sh https://bench.example.com slipstream-v0.2.0
#
# Run it from a SECOND machine in the same region, never on the target: a
# generator competing with the thing it measures produces numbers that describe
# neither. Results land in bench/results/<label>/ as one file per scenario plus
# a summary.tsv that diffs cleanly against another run.
#
# The target is expected to be a WordPress site with WooCommerce, matching the
# workload row in docs/benchmarks.md. PATH_CACHED is whatever page both stacks
# are compared on; a shop listing is the published choice.
set -eu

TARGET=${1:-}
LABEL=${2:-run}
if [ -z "$TARGET" ]; then
  echo "usage: $0 <https://target> [label]" >&2
  exit 2
fi

PATH_CACHED=${PATH_CACHED:-/shop/}
# A fixture of known size, not a WordPress file. Picking an asset out of
# wp-includes makes the static number depend on whichever version shipped it:
# dashicons.min.css is 59 KB, and at that size loopback saturates on bandwidth
# long before the server runs out of capacity, so the result describes the link
# rather than the panel. bench-static.txt is 1 KB, created by setup on both
# stacks. See bench/README.md.
PATH_STATIC=${PATH_STATIC:-/bench-static.txt}
DURATION=${DURATION:-60s}
SHORT=${SHORT:-30s}

command -v wrk >/dev/null 2>&1 || { echo "wrk is not installed" >&2; exit 1; }

# The flood scenario opens 2,000 connections. Under the default 1024 file
# descriptors, roughly 987 of them fail to open and wrk reports them as connect
# errors, which reads exactly like the server refusing load. It is the generator
# hitting its own limit: raise it or the flood measures this script.
# shellcheck disable=SC3045  # not in POSIX, but dash and bash both implement it,
# and the fallback below covers a shell that does not.
ulimit -n 65535 2>/dev/null || echo "warning: could not raise the fd limit; the flood result will measure it" >&2

here=$(cd -- "$(dirname -- "$0")" && pwd)
out="$here/results/$LABEL"
mkdir -p "$out"

# Accept-Encoding matters: the page cache stores responses pre-compressed, so a
# generator that does not ask for gzip measures a different cache entry from the
# one real browsers hit.
run() {
  name=$1
  url_path=$2
  shift 2
  echo "  $name"
  wrk "$@" -H 'Accept-Encoding: gzip' --latency "$TARGET$url_path" \
    > "$out/$name.txt" 2>&1 || true
}

echo "== $LABEL against $TARGET =="

run cached-sustained "$PATH_CACHED" -t4 -c500 -d"$DURATION"
run cached-single    "$PATH_CACHED" -t1 -c1 -d"$SHORT"
run cached-spike     "$PATH_CACHED" -t4 -c200 -d"$SHORT"
run flood            "$PATH_CACHED" -t8 -c2000 -d"$SHORT" --timeout 5s
run static           "$PATH_STATIC" -t4 -c200 -d"$SHORT"

# Uncacheable: the Lua script gives every request a unique query string, so this
# is the full PHP and database path rather than the cache.
run uncached "$PATH_CACHED" -t2 -c10 -d"$SHORT" -s "$here/wrk/uncached.lua"

# One line per scenario: requests/sec, p50, p99 and any socket errors, which is
# what the published table reports and what two runs get compared on.
summary="$out/summary.tsv"
printf 'scenario\treq_per_sec\tp50\tp99\terrors\n' > "$summary"
for name in cached-sustained cached-single cached-spike static flood uncached; do
  file="$out/$name.txt"
  [ -f "$file" ] || continue
  rps=$(awk '/Requests\/sec/ { print $2 }' "$file")
  p50=$(awk '/ 50%/ { print $2 }' "$file")
  p99=$(awk '/ 99%/ { print $2 }' "$file")
  err=$(awk '/Socket errors/ { sub(/^ *Socket errors: */, ""); print; exit }' "$file")
  printf '%s\t%s\t%s\t%s\t%s\n' "$name" "${rps:-na}" "${p50:-na}" "${p99:-na}" "${err:-none}" >> "$summary"
done

echo
cat "$summary"
echo
echo "raw output in $out"
