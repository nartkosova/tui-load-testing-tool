# Development diary

## 2026-10-12 — Core request generator

- Added configurable single-request HTTP execution and a CLI.
- Recorded status, duration, response bytes, and error type.
- Verified request behavior, timeout, cancellation, and CLI output with local tests.

## 2026-10-12 — Tooling update

- Updated the project to Go 1.27.2.
- Added Makefile commands for running, testing, formatting, checking, and building.
- Removed source comments and simplified the diary format.
- Verified `make check`, `make build`, and `make run` against a local server.

## 2026-10-15 — Concurrency patterns

- Added interchangeable worker-pool, fan-out/fan-in, and pipeline strategies in `internal/load`, reusing the existing HTTP request generator.
- Kept job and result queues bounded by concurrency, with a single owner of summary counters. Fan-in uses individual worker outputs and merging goroutines; the pipeline separates result classification into its own stage.
- Added request-count and duration limits, cancellation propagation, and result draining so every started request is accounted for before shutdown. Duration expiry is normal completion; external cancellation returns a partial summary and an error.
- Added CLI strategy selection, concurrency and workload flags, final summaries, and Ctrl+C/SIGTERM handling while retaining the original single-request mode.
- Verified configuration, exact request counts, simultaneous execution limits, repeated runs, deadlines, cancellation under backpressure, shared HTTP configuration, and CLI behavior with tests, race detection, and `go vet`.
- Built the binary and checked all three patterns against a local HTTP server, including exact request counts, duration expiry, SIGINT, and SIGTERM shutdown.

## 2026-10-26 — Metrics collection and latency measurement

- Moved summary aggregation into a shared collector (`internal/load/collector.go`) used by all three concurrency patterns; only the collector goroutine updates counters and histograms.
- Added request rate, execution error rate, and HTTP error rate to the summary. Rates report as unavailable when there are no requests or no measured duration. Rates use the measured `Runner.Run` duration, not the configured test duration.
- Added latency recording with HDR histograms (`github.com/HdrHistogram/hdrhistogram-go` v1.4.0). Separate distributions for successful responses, HTTP error responses (status 400+), and execution failures, each with sample count, rejected count, min, mean, p50, p95, p99, and max.
- Histogram configuration: microsecond unit, range 1µs–10min, 3 significant digits (relative error below 0.1%). Values outside the range are counted as rejected instead of being dropped silently. Empty distributions are shown as unavailable rather than zero.
- Extended the CLI summary with rate and latency lines.
- Verified counters (responses, HTTP errors, execution failures, body-read failures, cancellation), rates with a fixed duration (1000 requests / 2s = 500 req/s), and latency boundary cases (empty, single sample, range limits, negative values).
- Compared histogram percentiles against a sorted reference using the library's rank convention (rank = round(p/100·n), clamped to [1, n]) on repeated, wide-range (log-uniform 10µs–10s), and slow-tail datasets. All values stayed within the 0.1% + 1µs tolerance; e.g. slow-tail p95/p99: reference 29ms, histogram 29.007ms (absolute error 7µs, relative error 0.024%).
- Added `make bench`. Benchmark results (Go 1.27.2, linux/amd64, Intel i5-1235U, `-benchmem`):

  | Benchmark | Time/op | Memory/op | Allocs/op |
  |---|---|---|---|
  | Counters only | 4.57 ns | 0 B | 0 |
  | Counters + latency recording | 12.22 ns | 0 B | 0 |
  | Collector construction (3 histograms) | 141,954 ns | 516,869 B | 10 |
  | Summary calculation (4096 samples) | 245,505 ns | 0 B | 0 |

- Latency recording adds about 7.7 ns per result with no allocations. Histogram memory is fixed at roughly 170 KB per distribution regardless of sample count.
- Verified with tests, race detection, and `go vet`.

## 2026-10-26 — First pattern comparison

- Setup: small local Go HTTP server (`bench/server`) on `127.0.0.1:8099` with `/fast` (immediate `ok`) and `/slow` (10ms sleep). Load generator and server ran on the same laptop (i5-1235U, Go 1.27.2), so the numbers are only indicative.
- Command per run: `./bin/httpload -url http://127.0.0.1:8099/<endpoint> -pattern <pattern> -concurrency <10|100> -requests <n>` with 50,000 requests for `/fast` and 5,000 for `/slow`. Each combination ran 3 times; the table shows the median run. No errors in any run.

  | Endpoint | Conc. | Pattern | req/s | p50 | p99 |
  |---|---|---|---|---|---|
  | fast | 10 | worker-pool | 15,671 | 484µs | 2.20ms |
  | fast | 10 | fan-out-fan-in | 13,478 | 558µs | 2.60ms |
  | fast | 10 | pipeline | 14,743 | 509µs | 2.37ms |
  | fast | 100 | worker-pool | 33,580 | 2.02ms | 21.4ms |
  | fast | 100 | fan-out-fan-in | 35,706 | 1.81ms | 16.9ms |
  | fast | 100 | pipeline | 31,009 | 1.95ms | 27.3ms |
  | slow | 10 | worker-pool | 878 | 11.37ms | 12.75ms |
  | slow | 10 | fan-out-fan-in | 876 | 11.38ms | 12.87ms |
  | slow | 10 | pipeline | 871 | 11.43ms | 13.40ms |
  | slow | 100 | worker-pool | 7,632 | 11.21ms | 55.7ms |
  | slow | 100 | fan-out-fan-in | 8,091 | 11.26ms | 36.9ms |
  | slow | 100 | pipeline | 7,961 | 11.30ms | 44.4ms |

- Observations: with a 10ms server the patterns are practically identical — throughput is bound by concurrency × latency (~880 req/s at 10, ~8,000 at 100). On the fast endpoint the run-to-run variation (e.g. worker-pool at 10: 14,892–22,964 req/s) is larger than the differences between patterns, so no pattern can be called faster yet. Concurrency 100 mainly raises tail latency (p99), most likely from contention on the shared machine rather than the patterns themselves.
- Next: run the server on a separate machine, use more repetitions, and record CPU/memory of the load generator.

## 2026-10-26 — Extended pattern comparison

- **High concurrency + resources.** Same local server, `/fast`, 50,000 requests, 10 runs per combination via a small Python wrapper that runs `bin/httpload` and reads CPU time and peak memory (`os.wait4` rusage). Medians, no errors:

  | Conc. | Pattern | req/s (min–max) | CPU s | Peak RSS |
  |---|---|---|---|---|
  | 10 | worker-pool | 14,496 (13,088–15,736) | 20.3 | 19.6 MB |
  | 10 | fan-out-fan-in | 14,674 (13,601–19,551) | 20.2 | 19.5 MB |
  | 10 | pipeline | 14,455 (13,051–16,419) | 20.5 | 19.8 MB |
  | 100 | worker-pool | 31,858 (29,224–36,396) | 11.8 | 28.6 MB |
  | 100 | fan-out-fan-in | 29,733 (28,108–36,130) | 11.6 | 29.6 MB |
  | 100 | pipeline | 30,352 (28,946–34,389) | 12.0 | 29.1 MB |
  | 500 | worker-pool | 28,076 (27,028–33,748) | 13.5 | 58.4 MB |
  | 500 | fan-out-fan-in | 28,514 (27,635–32,704) | 13.2 | 60.0 MB |
  | 500 | pipeline | 27,578 (27,057–32,372) | 13.7 | 57.2 MB |
  | 1000 | worker-pool | 26,726 (24,996–34,185) | 14.1 | 94.3 MB |
  | 1000 | fan-out-fan-in | 26,673 (25,962–32,716) | 15.0 | 98.2 MB |
  | 1000 | pipeline | 27,492 (25,810–33,214) | 14.4 | 93.7 MB |

  Differences stay inside the run-to-run spread, also for CPU and memory. Throughput peaks around 100 and drops above it, while p99 grows (≈2ms at 10, ≈20–30ms at 100, ≈200–300ms at 1000) and memory grows with concurrency. Concurrency 10 used more CPU than 100, likely from connection churn: Go's default transport keeps only 2 idle connections per host — worth checking later.
- **Coordination overhead without network.** `BenchmarkPatternOverhead` (`internal/load/pattern_bench_test.go`) runs 10,000 requests against an instant fake sender, `-count 5`. Approx. ns per request: worker-pool 360 / 560 / 790, pipeline 425 / 655 / 820, fan-out-fan-in 520 / 775 / 1,005 at concurrency 10 / 100 / 1000. Worker-pool is cheapest and fan-out-fan-in costs most (an extra goroutine and channel per worker), but even 1µs is far below a real request (≥50µs locally), which is why it does not show up in HTTP runs.
- **Heavier processing stage.** `BenchmarkPatternProcessingCost` makes requests take 1ms (concurrency 100) and adds busy-work to result processing (`observe` is now a variable so the benchmark can wrap it). Medians in µs per request:

  | Processing cost | worker-pool | fan-out-fan-in | pipeline |
  |---|---|---|---|
  | 0 | 13.4 | 13.8 | 13.4 |
  | 20µs | 16.8 | 16.7 | 20.4 |
  | 100µs | 12.4 | 12.8 | 101.4 |

  This is the first clear difference: the pipeline runs processing in a single goroutine, so throughput is capped at 1/cost (≈9,900 req/s at 100µs), while worker-pool and fan-out-fan-in process inside the 100 workers in parallel. This confirms the bottleneck predicted in the pipeline design. (Busy-work at 100µs was slightly faster than 0 for the parallel patterns, probably because spinning workers avoid sleep/wake-up scheduling; not investigated.)
- **Conclusion so far:** for plain HTTP load the pattern does not affect throughput, latency, CPU, or memory measurably; the pipeline only becomes a bottleneck when result processing is expensive, unless that stage is parallelized.
- Run with: `make build`, `go run ./bench/server &`, `python3 bench/compare.py ./bin/httpload http://127.0.0.1:8099/fast 50000 10 10,100,500,1000`, and `go test -run XXX -bench Pattern -count 5 ./internal/load`.
