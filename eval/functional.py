import csv
import json
import sys
from pathlib import Path

COLUMNS = [
    "suite",
    "package",
    "test",
    "subtest",
    "outcome",
    "duration_s",
    "exit_code_of_suite",
    "log_ref",
    "log_line",
    "output_excerpt",
]


def convert(run_dir, suite, path, exit_code, rows):
    lines = Path(path).read_text().splitlines()
    outputs = {}
    first_line = {}
    for number, raw in enumerate(lines, 1):
        event = json.loads(raw)
        key = (event.get("Package"), event.get("Test"))
        if event.get("Action") == "output" and key[1]:
            outputs.setdefault(key, []).append(event["Output"].strip())
            first_line.setdefault(key, number)
        if event.get("Action") in ("pass", "fail", "skip"):
            pkg, test = key
            name, sub = (test.split("/", 1) + [None])[:2] if test else ("(package)", None)
            excerpt = " | ".join(o for o in outputs.get(key, []) if o and not o.startswith(("=== ", "--- ")))
            rows.append(
                {
                    "suite": suite,
                    "package": pkg,
                    "test": name,
                    "subtest": sub,
                    "outcome": event["Action"],
                    "duration_s": event.get("Elapsed"),
                    "exit_code_of_suite": exit_code,
                    "log_ref": str(Path(path).relative_to(run_dir)),
                    "log_line": first_line.get(key, number),
                    "output_excerpt": excerpt[:500] or None,
                }
            )


def main():
    run_dir = Path(sys.argv[1]).resolve()
    codes = json.loads(sys.argv[2])
    rows = []
    tests = run_dir / "raw" / "tests"
    convert(run_dir, "go test ./...", tests / "unit.jsonl", codes["unit"], rows)
    convert(run_dir, "go test -race ./...", tests / "race.jsonl", codes["race"], rows)
    vet_err = (tests / "vet.stderr").read_text().strip()
    rows.append(
        {
            "suite": "go vet ./...",
            "package": "./...",
            "test": "vet",
            "outcome": "pass" if codes["vet"] == 0 else "fail",
            "exit_code_of_suite": codes["vet"],
            "log_ref": "raw/tests/vet.stderr",
            "output_excerpt": vet_err or None,
        }
    )
    fmt = (tests / "gofmt.stdout").read_text().strip()
    rows.append(
        {
            "suite": "gofmt -l .",
            "package": "./...",
            "test": "gofmt",
            "outcome": "pass" if not fmt else "fail",
            "exit_code_of_suite": codes["gofmt"],
            "log_ref": "raw/tests/gofmt.stdout",
            "output_excerpt": fmt or None,
        }
    )
    for extra in json.loads(sys.argv[3]) if len(sys.argv) > 3 else []:
        rows.append(extra)
    with (run_dir / "functional_tests.csv").open("w", newline="") as f:
        w = csv.DictWriter(f, COLUMNS)
        w.writeheader()
        for r in rows:
            w.writerow({c: "NA" if r.get(c) is None else r.get(c) for c in COLUMNS})
    counts = {}
    for r in rows:
        counts[(r["suite"], r["outcome"])] = counts.get((r["suite"], r["outcome"]), 0) + 1
    print(counts)


if __name__ == "__main__":
    main()
