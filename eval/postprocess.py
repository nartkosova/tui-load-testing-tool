import csv
import sys
from collections import defaultdict
from pathlib import Path

RSS_FLOOR_MIB = 30.5
HEY_MAX_RESULTS = 1_000_000


def main():
    run_dir = Path(sys.argv[1]).resolve()
    sampled = defaultdict(float)
    with (run_dir / "resources.csv").open() as f:
        for r in csv.DictReader(f):
            if r["role"] == "generator" and r["rss_mib"] != "NA":
                sampled[r["run_id"]] = max(sampled[r["run_id"]], float(r["rss_mib"]))
    path = run_dir / "results.csv"
    with path.open() as f:
        reader = csv.DictReader(f)
        columns = list(reader.fieldnames)
        rows = list(reader)
    extra = [
        "generator_rss_sampled_max_mib",
        "generator_peak_rss_floor_affected",
        "generator_peak_rss_best_mib",
        "hey_result_cap_reached",
    ]
    for c in extra:
        if c not in columns:
            columns.insert(columns.index("generator_peak_rss_mib") + 1 + extra.index(c), c)
    for r in rows:
        s = sampled.get(r["run_id"])
        r["generator_rss_sampled_max_mib"] = f"{s:.3f}" if s else "NA"
        try:
            ru = float(r["generator_peak_rss_mib"])
        except ValueError:
            ru = None
        affected = ru is not None and ru <= RSS_FLOOR_MIB
        r["generator_peak_rss_floor_affected"] = "true" if affected else "false"
        if affected:
            r["generator_peak_rss_best_mib"] = r["generator_rss_sampled_max_mib"]
        else:
            r["generator_peak_rss_best_mib"] = r["generator_peak_rss_mib"]
        cap = (
            r["tool"] == "hey"
            and r["responses_completed"] not in ("NA", "")
            and int(r["responses_completed"]) >= HEY_MAX_RESULTS
        )
        r["hey_result_cap_reached"] = "true" if cap else ("false" if r["tool"] == "hey" else "NA")
        if cap and "hey result cap" not in r["notes"]:
            note = "hey result cap (1,000,000 stored results) reached: counts and rps truncated, lower bound only"
            r["notes"] = note if r["notes"] in ("NA", "") else f"{r['notes']}; {note}"
    with path.open("w", newline="") as f:
        w = csv.DictWriter(f, columns)
        w.writeheader()
        w.writerows(rows)
    print(
        f"rows={len(rows)} floor_affected={sum(r['generator_peak_rss_floor_affected'] == 'true' for r in rows)} "
        f"hey_capped={sum(r['hey_result_cap_reached'] == 'true' for r in rows)}"
    )


if __name__ == "__main__":
    main()
