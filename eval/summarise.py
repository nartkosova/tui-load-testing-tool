import csv
import itertools
import math
import statistics
import sys
from pathlib import Path

METRICS = [
    "completed_rps",
    "reported_rps",
    "p50_ms",
    "p95_ms",
    "p99_ms",
    "max_ms",
    "execution_errors",
    "http_errors",
    "generator_cpu_user_s",
    "generator_cpu_system_s",
    "generator_cpu_util_pct_alloc",
    "generator_cpu_us_per_response",
    "generator_peak_rss_mib",
    "generator_peak_rss_best_mib",
    "generator_rss_sampled_max_mib",
    "target_cpu_util_pct_alloc",
    "target_cpu_us_per_response",
    "target_peak_rss_mib",
    "tcp_active_opens",
    "wall_duration_s",
    "reported_duration_s",
    "active_cpu_s",
    "active_window_s",
    "tui_metric_updates",
    "tui_metric_gap_p50_ms",
    "tui_metric_gap_p95_ms",
    "tui_metric_gap_max_ms",
    "tui_output_gap_p50_ms",
    "tui_output_gap_max_ms",
    "tui_first_update_ms",
    "cancel_response_ms",
    "post_output_exit_ms",
]
KEYS = ["experiment_group", "tool", "pattern", "endpoint", "concurrency", "tui_enabled", "sqlite_enabled"]


CAPPED_METRICS = {"completed_rps", "reported_rps", "generator_cpu_us_per_response", "target_cpu_us_per_response"}


def usable(r, metric):
    return r["status"] == "ok" and not (metric in CAPPED_METRICS and r.get("hey_result_cap_reached") == "true")


def num(v):
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


def describe(values):
    values = sorted(v for v in values if v is not None)
    if not values:
        return {"n": 0}
    q = statistics.quantiles(values, n=4, method="inclusive") if len(values) > 1 else [values[0]] * 3
    mean = statistics.fmean(values)
    sd = statistics.stdev(values) if len(values) > 1 else 0.0
    return {
        "n": len(values),
        "median": statistics.median(values),
        "q1": q[0],
        "q3": q[2],
        "min": values[0],
        "max": values[-1],
        "mean": mean,
        "sd": sd,
        "cv_pct": 100 * sd / mean if mean else None,
    }


def mann_whitney_exact(a, b):
    a, b = list(a), list(b)
    if not a or not b:
        return None
    pooled = a + b
    ranks = {}
    ordered = sorted(pooled)
    i = 0
    while i < len(ordered):
        j = i
        while j < len(ordered) and ordered[j] == ordered[i]:
            j += 1
        ranks[ordered[i]] = (i + j + 1) / 2
        i = j
    r = [ranks[v] for v in pooled]
    n1 = len(a)
    observed = sum(r[:n1]) - n1 * (n1 + 1) / 2
    mean_u = n1 * len(b) / 2
    count = extreme = 0
    for combo in itertools.combinations(range(len(pooled)), n1):
        u = sum(r[k] for k in combo) - n1 * (n1 + 1) / 2
        count += 1
        if abs(u - mean_u) >= abs(observed - mean_u) - 1e-9:
            extreme += 1
    return observed, extreme / count


def holm(pvalues):
    order = sorted(range(len(pvalues)), key=lambda i: pvalues[i])
    adjusted = [None] * len(pvalues)
    running = 0.0
    m = len(pvalues)
    for rank, i in enumerate(order):
        running = max(running, min(1.0, (m - rank) * pvalues[i]))
        adjusted[i] = running
    return adjusted


def load(path):
    with open(path) as f:
        return list(csv.DictReader(f))


def write(path, rows, columns):
    with open(path, "w", newline="") as f:
        w = csv.DictWriter(f, columns)
        w.writeheader()
        for r in rows:
            w.writerow({c: fmt(r.get(c)) for c in columns})


def fmt(v):
    if v is None:
        return "NA"
    if isinstance(v, float):
        if math.isnan(v):
            return "NA"
        return f"{v:.6g}"
    return v


def main():
    run_dir = Path(sys.argv[1]).resolve()
    out_dir = run_dir / "summary"
    out_dir.mkdir(exist_ok=True)
    rows = [r for r in load(run_dir / "results.csv") if r["experiment_group"] != "F_monitor"]
    status_counts = {}
    for r in rows:
        k = (r["experiment_group"], r["status"])
        status_counts[k] = status_counts.get(k, 0) + 1
    groups = {}
    for r in rows:
        groups.setdefault(tuple(r[k] for k in KEYS), []).append(r)
    summary = []
    for key, items in sorted(groups.items()):
        ok = [r for r in items if r["status"] == "ok"]
        for metric in METRICS:
            d = describe(num(r[metric]) for r in ok if usable(r, metric))
            if d["n"] == 0:
                continue
            summary.append(dict(zip(KEYS, key), metric=metric, runs_total=len(items), runs_ok=len(ok), **d))
    write(
        out_dir / "summary_by_config.csv",
        summary,
        KEYS + ["metric", "runs_total", "runs_ok", "n", "median", "q1", "q3", "min", "max", "mean", "sd", "cv_pct"],
    )
    tests = []

    def compare(family, label_a, items_a, label_b, items_b, metric, context):
        a = [num(r[metric]) for r in items_a if usable(r, metric) and num(r[metric]) is not None]
        b = [num(r[metric]) for r in items_b if usable(r, metric) and num(r[metric]) is not None]
        res = mann_whitney_exact(a, b)
        if res is None:
            return
        ma, mb = statistics.median(a), statistics.median(b)
        overlap = not (max(a) < min(b) or max(b) < min(a))
        tests.append(
            {
                "family": family,
                "context": context,
                "metric": metric,
                "a": label_a,
                "b": label_b,
                "n_a": len(a),
                "n_b": len(b),
                "median_a": ma,
                "median_b": mb,
                "median_ratio_b_over_a": mb / ma if ma else None,
                "ranges_overlap": overlap,
                "mwu_u": res[0],
                "mwu_p_exact_two_sided": res[1],
            }
        )

    by = {}
    for r in rows:
        by.setdefault(
            (
                r["experiment_group"],
                r["tool"],
                r["pattern"],
                r["endpoint"],
                r["concurrency"],
                r["tui_enabled"],
                r["sqlite_enabled"],
            ),
            [],
        ).append(r)
    for metric in ("completed_rps", "p99_ms", "generator_cpu_us_per_response", "generator_peak_rss_best_mib"):
        for e in ("/fast", "/slow"):
            for c in ("10", "100", "1000"):
                for pa, pb in itertools.combinations(["worker-pool", "fan-out-fan-in", "pipeline"], 2):
                    ia = by.get(("A_patterns", "httpload", pa, e, c, "false", "false"), [])
                    ib = by.get(("A_patterns", "httpload", pb, e, c, "false", "false"), [])
                    compare(f"A_patterns:{metric}", pa, ia, pb, ib, metric, f"{e} c={c}")
        for e in ("/fast", "/slow"):
            for c in ("100", "1000"):
                base = [
                    r
                    for k, v in by.items()
                    if k[0] == "A_patterns" and k[2] == "worker-pool" and k[3] == e and k[4] == c
                    for r in v
                ]
                for t in ("k6", "wrk", "hey"):
                    other = [
                        r
                        for k, v in by.items()
                        if k[0] == "B_tools" and k[1] == t and k[3] == e and k[4] == c
                        for r in v
                    ]
                    compare(f"B_tools:{metric}", "httpload(worker-pool)", base, t, other, metric, f"{e} c={c}")
    for metric in (
        "completed_rps",
        "p99_ms",
        "generator_cpu_us_per_response",
        "generator_peak_rss_best_mib",
        "generator_cpu_util_pct_alloc",
    ):
        for p in ("worker-pool", "fan-out-fan-in", "pipeline"):
            for c in ("100", "1000"):
                ia = by.get(("A_patterns", "httpload", p, "/fast", c, "false", "false"), [])
                ib = by.get(("C_tui", "httpload", p, "/fast", c, "true", "false"), [])
                compare(f"C_tui:{metric}", "cli", ia, "tui", ib, metric, f"{p} /fast c={c}")
    for metric in (
        "completed_rps",
        "wall_duration_s",
        "post_output_exit_ms",
        "generator_peak_rss_mib",
        "generator_cpu_us_per_response",
    ):
        for c in ("100", "1000"):
            ia = by.get(("E_sqlite", "httpload", "worker-pool", "/fast", c, "false", "false"), [])
            ib = by.get(("E_sqlite", "httpload", "worker-pool", "/fast", c, "false", "true"), [])
            compare(f"E_sqlite:{metric}", "no-db", ia, "db", ib, metric, f"worker-pool /fast c={c}")
    families = {}
    for i, t in enumerate(tests):
        families.setdefault(t["family"], []).append(i)
    for idx in families.values():
        adj = holm([tests[i]["mwu_p_exact_two_sided"] for i in idx])
        for i, p in zip(idx, adj):
            tests[i]["holm_adjusted_p"] = p
    write(
        out_dir / "comparisons.csv",
        tests,
        [
            "family",
            "context",
            "metric",
            "a",
            "b",
            "n_a",
            "n_b",
            "median_a",
            "median_b",
            "median_ratio_b_over_a",
            "ranges_overlap",
            "mwu_u",
            "mwu_p_exact_two_sided",
            "holm_adjusted_p",
        ],
    )
    write(
        out_dir / "status_counts.csv",
        [{"experiment_group": k[0], "status": k[1], "runs": v} for k, v in sorted(status_counts.items())],
        ["experiment_group", "status", "runs"],
    )
    print(f"summary rows={len(summary)} comparisons={len(tests)} status={status_counts}")


if __name__ == "__main__":
    main()
