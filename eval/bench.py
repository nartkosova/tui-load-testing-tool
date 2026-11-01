import csv
import re
import subprocess
import sys
import time
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
GEN_CPUS = "4-7"
COLUMNS = [
    "benchmark",
    "case",
    "procs",
    "repetition",
    "iterations",
    "ns_per_op",
    "bytes_per_op",
    "allocs_per_op",
    "ns_per_request",
    "op_meaning",
    "category",
    "raw_log",
    "raw_line",
]
MEANING = {
    "BenchmarkPatternOverhead": (
        "one Runner.Run of 10000 fake requests (instant sender)",
        "fake-sender coordination overhead",
    ),
    "BenchmarkPatternProcessingCost": (
        "one Runner.Run of 10000 fake requests, 1 ms sleeping sender, c=100, busy-wait processing cost per result",
        "fake-sender processing placement",
    ),
    "BenchmarkCollectorCountersOnly": ("one inline counter update (not via collector.record)", "collector"),
    "BenchmarkCollectorWithLatency": ("one collector.record incl. HDR RecordValue", "collector"),
    "BenchmarkCollectorConstruction": ("one newCollector (three HDR histograms)", "collector"),
    "BenchmarkCollectorSummary": ("one collector.result: three HDR stats incl. percentiles and buckets", "collector"),
}
LINE = re.compile(r"^(Benchmark\w+)(?:/(\S+?))?(?:-(\d+))?\s+(\d+)\s+(.*)$")


def main():
    run_dir = Path(sys.argv[1]).resolve()
    count = int(sys.argv[2]) if len(sys.argv) > 2 else 5
    raw = run_dir / "raw" / "bench"
    raw.mkdir(parents=True, exist_ok=True)
    cmd = [
        "taskset",
        "-c",
        GEN_CPUS,
        "go",
        "test",
        "-run",
        "^$",
        "-bench",
        ".",
        "-benchmem",
        "-count",
        str(count),
        "-timeout",
        "60m",
        "./internal/load/",
    ]
    (raw / "bench_command.txt").write_text(" ".join(cmd) + "\n")
    start = time.time()
    with (raw / "bench.stdout").open("w") as out, (raw / "bench.stderr").open("w") as err:
        code = subprocess.run(cmd, cwd=REPO, stdout=out, stderr=err, timeout=3600).returncode
    (raw / "bench_exit_code.txt").write_text(f"{code}\nwall_s={time.time() - start:.1f}\n")
    reps = {}
    rows = []
    for number, line in enumerate((raw / "bench.stdout").read_text().splitlines(), 1):
        m = LINE.match(line)
        if not m:
            continue
        name, case, procs, iters, rest = m.groups()
        metrics = dict((unit, float(v)) for v, unit in re.findall(r"([0-9.]+) (\S+)", rest))
        key = (name, case)
        reps[key] = reps.get(key, 0) + 1
        meaning, category = MEANING.get(name, ("unknown", "unknown"))
        rows.append(
            {
                "benchmark": name,
                "case": case,
                "procs": procs,
                "repetition": reps[key],
                "iterations": int(iters),
                "ns_per_op": metrics.get("ns/op"),
                "bytes_per_op": metrics.get("B/op"),
                "allocs_per_op": metrics.get("allocs/op"),
                "ns_per_request": metrics.get("ns/request"),
                "op_meaning": meaning,
                "category": category,
                "raw_log": "raw/bench/bench.stdout",
                "raw_line": number,
            }
        )
    with (run_dir / "benchmarks.csv").open("w", newline="") as f:
        w = csv.DictWriter(f, COLUMNS)
        w.writeheader()
        for r in rows:
            w.writerow({c: "NA" if r.get(c) is None else r[c] for c in COLUMNS})
    test_bin = raw / "load.test"
    subprocess.run(["go", "test", "-c", "-o", str(test_bin), "./internal/load/"], cwd=REPO, check=True, timeout=600)
    for sym in ("BenchmarkCollectorCountersOnly", "BenchmarkCollectorWithLatency", "BenchmarkCollectorSummary"):
        dump = subprocess.run(
            [
                "go",
                "tool",
                "objdump",
                "-s",
                f"^github.com/nartkosova/tui-load-testing-tool/internal/load.{sym}$",
                str(test_bin),
            ],
            cwd=REPO,
            capture_output=True,
            text=True,
            timeout=600,
        ).stdout
        (raw / f"objdump_{sym}.txt").write_text(dump)
    test_bin.unlink()
    print(f"exit={code} rows={len(rows)}")


if __name__ == "__main__":
    main()
