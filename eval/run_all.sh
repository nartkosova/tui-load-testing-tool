#!/usr/bin/env bash
set -u
RUN="$1"
LOG="$RUN/raw/suite.log"
cd "$(dirname "$0")/.."
stamp() { date -u +%Y-%m-%dT%H:%M:%SZ; }
power() { echo "$(stamp) power ac_online=$(cat /sys/class/power_supply/ADP1/online) battery=$(cat /sys/class/power_supply/BAT0/status) loadavg=$(cut -d' ' -f1-3 /proc/loadavg)" >> "$LOG"; }
step() {
  local name="$1"; shift
  power
  echo "$(stamp) start $name: $*" >> "$LOG"
  "$@" >> "$LOG" 2>&1
  local code=$?
  echo "$(stamp) end $name exit=$code" >> "$LOG"
}
step http timeout 18000 python3 eval/harness.py http --run-dir "$RUN" --groups A_patterns,B_tools,C_tui,D_cancel,E_sqlite --reps 3 --warmup 3 --measure 10
step monitor timeout 1800 python3 eval/harness.py monitor --run-dir "$RUN" --reps 2
step bench timeout 3600 python3 eval/bench.py "$RUN" 5
step latency timeout 900 taskset -c 4-7 "$RUN/bin/latencyacc" -out "$RUN/latency_accuracy.csv"
power
echo "$(stamp) suite done" >> "$LOG"
