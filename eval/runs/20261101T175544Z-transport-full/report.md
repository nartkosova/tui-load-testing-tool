# Rerun with connection reuse: httpload adc9ddc

## Scope and change

**What changed.** The only code change is in `request.Generator` (`raw/generator_change.diff`). The binary was built from 8ab89bc plus that diff before it was committed; the diff was then committed unchanged as **adc9ddc** (verified byte-identical), so these results describe adc9ddc. The generator now uses a clone of `http.DefaultTransport` with `MaxIdleConns = 0` and `MaxIdleConnsPerHost = 10000`, replacing Go's default of 2 idle connections per host. No other production code differs from e402cc8, the version evaluated in `../20261101T155011Z/`.

**What was rerun.** Groups A, C, D and E were rerun for httpload only, with the same plan, seed, protocol, target image, pinning and machine as the baseline:

- 3 repetitions of a 3 s warm-up plus a 10 s measured run;
- group D: concurrency 1000 with 2 repetitions.

In total 96 runs were made, all with status ok. The D runs exited with code 1, which is the expected result for a cancellation.

**Checks on the change.** `go test ./...`, `go vet ./...` and `gofmt` all passed.

**What was not rerun:**
- **Group B** (k6, wrk, hey): those tools did not change, so their baseline results are reused for comparison. They come from a different time block (about 1 h earlier).
- **Monitoring:** the monitor already reused its single connection.
- **Microbenchmarks and latency accuracy:** these use fake senders and do not touch the transport.

The column definitions, measurement boundaries and limitations are the same as in `../20261101T155011Z/report.md`.

## Group A: before (e402cc8) → after; median of 3 runs, range of the new runs

| Endpoint | c | Pattern | completed rps | p99 ms | generator CPU µs/resp | TCP opens per run |
|---|---|---|---|---|---|---|
| /fast | 10 | worker-pool | 47 432 → 107 183 [94 477–119 232] | 1.44 → 0.41 | 78.1 → 32.2 | 143 492 → 10 |
| /fast | 10 | fan-out-fan-in | 43 876 → 100 230 [99 428–100 355] | 1.06 → 0.45 | 86.4 → 37.0 | 142 718 → 13 |
| /fast | 10 | pipeline | 46 796 → 97 860 [95 348–114 588] | 0.96 → 0.46 | 79.7 → 35.5 | 143 014 → 12 |
| /fast | 100 | worker-pool | 24 445 → 97 633 [95 717–119 634] | 50.11 → 3.62 | 156.4 → 39.6 | 145 579 → 161 |
| /fast | 100 | fan-out-fan-in | 25 285 → 95 898 [94 172–105 841] | 46.75 → 3.58 | 151.6 → 40.3 | 146 198 → 138 |
| /fast | 100 | pipeline | 24 385 → 97 377 [91 270–115 593] | 50.78 → 3.63 | 156.6 → 39.7 | 146 396 → 144 |
| /fast | 1000 | worker-pool | 17 973 → 89 200 [82 488–96 045] | 449.02 → 26.69 | 215.7 → 42.6 | 139 304 → 1 220 |
| /fast | 1000 | fan-out-fan-in | 17 804 → 80 180 [79 361–99 878] | 459.01 → 27.65 | 216.2 → 47.5 | 138 422 → 1 774 |
| /fast | 1000 | pipeline | 17 511 → 82 365 [82 080–99 327] | 460.03 → 29.76 | 220.9 → 46.3 | 138 270 → 1 556 |
| /slow | 10 | worker-pool | 873 → 867 [864–868] | 12.62 → 12.35 | 379.2 → 265.1 | 3 666 → 11 |
| /slow | 10 | fan-out-fan-in | 867 → 865 [865–867] | 12.75 → 12.34 | 414.7 → 268.7 | 3 632 → 11 |
| /slow | 10 | pipeline | 864 → 863 [863–873] | 12.63 → 12.38 | 413.9 → 271.3 | 3 705 → 10 |
| /slow | 100 | worker-pool | 8 999 → 8 870 [8 864–8 903] | 14.38 → 14.56 | 202.7 → 137.4 | 31 022 → 155 |
| /slow | 100 | fan-out-fan-in | 8 979 → 8 899 [8 893–8 908] | 14.64 → 14.68 | 203.0 → 142.2 | 30 973 → 114 |
| /slow | 100 | pipeline | 8 967 → 8 871 [8 869–8 875] | 14.45 → 14.58 | 204.0 → 140.0 | 31 204 → 101 |
| /slow | 1000 | worker-pool | 18 379 → 82 291 [79 147–82 373] | 457.98 → 21.04 | 209.2 → 37.2 | 142 758 → 1 234 |
| /slow | 1000 | fan-out-fan-in | 18 559 → 81 600 [80 660–81 887] | 460.03 → 21.28 | 208.1 → 37.9 | 142 360 → 1 276 |
| /slow | 1000 | pipeline | 18 659 → 82 020 [81 992–82 283] | 476.16 → 21.15 | 206.8 → 37.4 | 143 268 → 1 423 |

**Throughput and CPU:**
- /fast throughput rose by **2.1–5.0×**, and /slow c=1000 throughput by **4.4–4.5×**.
- p99 fell by **2–23×**, and CPU per response by **1.4–5.6×**.
- TCP opens per run fell from 31 000–146 000 to 10–1 800.

**Saturation:**
- The generator is still CPU-bound on /fast (86–97 % of its 4 CPUs).
- /slow c=1000 is no longer generator-bound: the generator uses about 77 % of its CPUs, and throughput (~82 000 rps) approaches the server-delay bound.
- The target now uses 56–65 % of its 4 CPUs, against 30–53 % before.

**Small regressions:**
- /slow c=100 throughput is **1.0–1.4 % lower** (non-overlapping ranges), and p50 rose from 10.8 ms to 11.1 ms. There is no explanation for this yet. The size is comparable to the ~3 % time-block effect seen in the baseline.
- At /slow c=10 and c=100, CPU per response fell by about 32–36 %.

**Remaining execution errors** are duration-expiry interruptions only: no more than the concurrency in every run, and faster than the timeout.

**Memory** is essentially unchanged: 19–25 MiB at c ≤ 100 and 85–112 MiB at c=1000.

**Patterns:** they are again within about 10 % of each other, inconsistently in direction. With reuse at c=1000, worker-pool was highest on /fast and all three were equal on /slow. Ranges overlap in most cells, so this still does **not support a pattern ranking**.

## Comparison with the other tools (B values from the baseline run; hey /fast from the uncapped 5 s supplement)

| Endpoint | c | httpload (now) | k6 | wrk | hey |
|---|---|---|---|---|---|
| /fast | 100 | 97 633 rps · 39.6 µs/resp · p99 3.62 · 24.9 MiB | 50 533 · 78.8 · 10.23 · 193.9 | 227 442 · 9.1 · 1.70 · 6.0 | 101 642 · 39.1 · 3.40 · 160.3 |
| /fast | 1000 | 89 200 · 42.6 · 26.69 · 95.0 | 42 481 · 93.0 · 69.35 · 388.3 | 167 491 · 12.2 · 14.88 · 10.4 | 82 241 · 47.5 · 28.10 · 221.8 |
| /slow | 100 | 8 870 · 137.4 · 14.56 · 25.2 | 8 998 · 185.5 · 15.39 · 90.6 | 8 751 · 44.3 · 13.32 · 5.9 | 8 918 · 123.2 · 13.30 · 93.9 |
| /slow | 1000 | 82 291 · 37.2 · 21.04 · 85.1 | 38 560 · 102.9 · 76.63 · 386.3 | 85 208 · 15.5 · 17.06 · 10.2 | 84 072 · 37.1 · 21.80 · 268.7 |

How httpload now compares:
- **Against hey:** about equal in throughput, CPU per response and p99. At /fast c=1000 it is slightly ahead (89 200 vs 82 241 rps).
- **Against k6:** about 2× the throughput, with half the CPU per response.
- **Against wrk:** still behind. wrk needs 3–4× less CPU per response and is target-limited on /fast.
- **Memory:** 2–8× less than k6 and hey; wrk is lowest.

The cross-tool comparison spans two time blocks, so differences under about 5 % should not be interpreted.

## Group C: TUI vs CLI, both on the new build

| Pattern | c | completed rps CLI → TUI | p99 ms CLI → TUI | µs/resp CLI → TUI | RSS MiB CLI → TUI | metric-update gap p50 / p95 / max (ms) |
|---|---|---|---|---|---|---|
| worker-pool | 100 | 97 633 → 96 031 | 3.62 → 3.81 | 39.6 → 40.4 | 24.9 → 40.9 | 100 / 100 / 101 |
| fan-out-fan-in | 100 | 95 898 → 95 720 | 3.58 → 3.76 | 40.3 → 40.9 | 24.7 → 44.1 | 100 / 101 / 103 |
| pipeline | 100 | 97 377 → 97 638 | 3.63 → 3.72 | 39.7 → 39.9 | 25.4 → 44.2 | 100 / 100 / 101 |
| worker-pool | 1000 | 89 200 → 82 061 | 26.69 → 31.23 | 42.6 → 46.4 | 95.0 → 116.2 | 100 / 106 / 221 |
| fan-out-fan-in | 1000 | 80 180 → 79 611 | 27.65 → 30.40 | 47.5 → 47.8 | 111.7 → 114.5 | 100 / 109 / 207 |
| pipeline | 1000 | 82 365 → 82 521 | 29.76 → 32.00 | 46.3 → 46.2 | 103.5 → 113.6 | 100 / 102 / 190 |

**TUI cost:**
- It is at most about 2 % in throughput, except worker-pool c=1000 at −8 %, where the CLI range is wide (82 488–96 045).
- p99 is 2–17 % higher with the TUI.
- Memory is 3–21 MiB higher at c=1000 and 16–19 MiB higher at c=100.

**Update timing improved.** Fresh metric updates now arrive almost exactly every 100 ms: the worst gap is 221 ms, against up to about 600 ms before. Before the change, at c=1000 about 5–12 % of updates were delayed or dropped; now about 100 per 10 s are delivered in every configuration.

The generator is still CPU-saturated, so TUI overhead cannot be fully isolated.

## Group D: cancellation (c=1000)

| Mode | worker-pool | fan-out-fan-in | pipeline | Before the change |
|---|---|---|---|---|
| CLI SIGINT → exit | 22.8–24.9 ms | 20.2–24.6 ms | 20.6–25.1 ms | 26–353 ms |
| TUI `q` → stopped screen | 30.3 ms | 29.6–30.3 ms | 29.3–40.5 ms | 30–57 ms |

Every run exited with code 1 and the final summary was available.

## Group E: SQLite (worker-pool /fast)

| c | completed rps off → on | Last output → exit, ms (off → on) | DB row matches summary |
|---|---|---|---|
| 100 | 97 200 → 96 187 | 2.2 → 19.0 | 3/3 |
| 1000 | 82 586 → 82 308 | 6.9 → 26.6 | 3/3 |

Throughput ranges overlap. The post-run write adds about 17–20 ms, as in the baseline.

## Conclusion

Replacing the default 2-idle-connection pool with per-host reuse is responsible for most of httpload's previous gap to the Go-based tools. It reduced connection set-up by 2–4 orders of magnitude, raised throughput 2–5× and cut p99 latency up to 23×. Memory use, correctness of counts, cancellation and SQLite behaviour did not change.

This is a single rerun block with 3 repetitions. Statistical significance cannot be claimed. The before/after differences are, however, far larger than any run-to-run spread observed.
