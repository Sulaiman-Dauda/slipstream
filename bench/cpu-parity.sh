#!/bin/sh
# Prove two runs happened on comparable hardware before comparing their numbers.
#
# Two "identical" VPS instances of the same spec once measured 2.5x apart on
# this loop, and a later pair differed by 16%. That is larger than most effects
# a panel comparison is trying to measure, which is why the benchmark runs both
# panels on one box in turn rather than on two boxes at once. Run this before
# and after each stack: if the numbers move, the box moved, and the comparison
# is void whatever the throughput said.
set -eu

loops=${LOOPS:-20000000}

start=$(date +%s.%N)
awk -v n="$loops" 'BEGIN { s = 0; for (i = 0; i < n; i++) s += i % 7; print s > "/dev/null" }'
end=$(date +%s.%N)

secs=$(awk -v a="$start" -v b="$end" 'BEGIN { printf "%.2f", b - a }')

# Steal is the other half of the question: a fast loop on a noisy hypervisor is
# still a bad baseline. vmstat's last column is st.
steal=$(vmstat 1 3 2>/dev/null | tail -1 | awk '{print $NF}')

printf 'cpu_loop_seconds %s\n' "$secs"
printf 'steal_percent %s\n' "${steal:-unknown}"
printf 'model %s\n' "$(awk -F: '/model name/ { sub(/^ /, "", $2); print $2; exit }' /proc/cpuinfo)"
printf 'cores %s\n' "$(nproc)"

if [ "${steal:-0}" != "0" ] && [ "${steal:-0}" != "unknown" ]; then
  echo "steal is non-zero: this box is sharing CPU and its numbers are not comparable" >&2
  exit 1
fi
