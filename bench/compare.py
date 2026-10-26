# Runs bin/httpload for every pattern and reports throughput, CPU time and peak memory.
# Usage: python3 bench/compare.py <binary> <url> <requests> <runs> <concurrency,...>
import os, subprocess, sys, re, statistics as st
binary, url, reqs, reps = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4])
for c in sys.argv[5].split(","):
    for p in ["worker-pool", "fan-out-fan-in", "pipeline"]:
        rows = []
        for _ in range(reps):
            proc = subprocess.Popen([binary, "-url", url, "-pattern", p, "-concurrency", c, "-requests", reqs], stdout=subprocess.PIPE, text=True)
            out = proc.stdout.read()
            _, _, ru = os.wait4(proc.pid, 0)
            proc.returncode = 0
            rate = float(re.search(r"requests/sec: ([\d.]+)", out).group(1))
            p99 = re.search(r"success latency: .*p99=(\S+)", out).group(1)
            errs = int(re.search(r"^errors: (\d+)", out, re.M).group(1))
            rows.append((rate, ru.ru_utime + ru.ru_stime, ru.ru_maxrss / 1024, p99, errs))
        rates = [r[0] for r in rows]
        print(f"c={c} {p}: req/s med={st.median(rates):.0f} min={min(rates):.0f} max={max(rates):.0f} sd={st.stdev(rates):.0f} | cpu-s med={st.median(r[1] for r in rows):.2f} | rss-MB med={st.median(r[2] for r in rows):.1f} | p99={sorted(r[3] for r in rows)} | errors={sum(r[4] for r in rows)}", flush=True)
