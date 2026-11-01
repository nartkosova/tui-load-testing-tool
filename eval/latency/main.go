package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"

	"github.com/nartkosova/tui-load-testing-tool/internal/load"
	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

const seed = 20261101

type replay struct {
	samples []time.Duration
	next    atomic.Int64
}

func (r *replay) Send(context.Context) request.Result {
	i := r.next.Add(1) - 1
	return request.Result{StatusCode: 200, Duration: r.samples[i]}
}

type dataset struct {
	name     string
	generate func(rng *rand.Rand, n int) []time.Duration
}

var datasets = []dataset{
	{"repeated", func(rng *rand.Rand, n int) []time.Duration {
		cycle := []time.Duration{2 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond, 1234567 * time.Nanosecond}
		values := make([]time.Duration, n)
		for i := range values {
			values[i] = cycle[i%len(cycle)]
		}
		rng.Shuffle(n, func(i, j int) { values[i], values[j] = values[j], values[i] })
		return values
	}},
	{"wide_range", func(rng *rand.Rand, n int) []time.Duration {
		values := make([]time.Duration, n)
		for i := range values {
			values[i] = time.Duration(math.Pow(10, 4+6*rng.Float64()))
		}
		return values
	}},
	{"slow_tail", func(rng *rand.Rand, n int) []time.Duration {
		values := make([]time.Duration, n)
		for i := range values {
			if i%100 == 99 {
				values[i] = 2*time.Second + time.Duration(rng.Int64N(int64(3*time.Second)))
			} else {
				values[i] = 20*time.Millisecond + time.Duration(rng.Int64N(int64(10*time.Millisecond)))
			}
		}
		rng.Shuffle(n, func(i, j int) { values[i], values[j] = values[j], values[i] })
		return values
	}},
	{"out_of_range", func(rng *rand.Rand, n int) []time.Duration {
		values := make([]time.Duration, n)
		for i := range values {
			switch i % 100 {
			case 98:
				values[i] = 400 * time.Nanosecond
			case 99:
				values[i] = 11*time.Minute + time.Duration(rng.Int64N(int64(time.Minute)))
			default:
				values[i] = time.Millisecond + time.Duration(rng.Int64N(int64(99*time.Millisecond)))
			}
		}
		rng.Shuffle(n, func(i, j int) { values[i], values[j] = values[j], values[i] })
		return values
	}},
}

func hdrRank(p float64, n int) int {
	rank := p/100*float64(n) + 0.5
	if !(rank >= 1) {
		return 1
	}
	if rank >= float64(n) {
		return n
	}
	return int(rank)
}

func ceilRank(p float64, n int) int {
	return max(1, min(n, int(math.Ceil(p/100*float64(n)))))
}

func us(d time.Duration) float64 {
	return float64(d) / float64(time.Microsecond)
}

func main() {
	out := flag.String("out", "latency_accuracy.csv", "output CSV")
	flag.Parse()
	file, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	w := csv.NewWriter(file)
	w.Write([]string{"dataset", "n_samples", "seed", "statistic", "rank_hdr", "ref_exact_us", "ref_truncated_us",
		"hist_us", "abs_error_us", "rel_error_pct", "quantisation_error_us", "histogram_error_us",
		"ref_ceil_rank_us", "rank_convention_diff_us", "recorded_count", "rejected_count", "expected_rejected",
		"expected_recorded", "documented_range_exceeded", "recording_unit", "lowest_us", "highest_us", "significant_digits", "replay_wall_ms"})
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }
	for di, ds := range datasets {
		for _, n := range []int{1_000, 10_000, 100_000, 1_000_000} {
			streamSeed := uint64(seed + di*10_000_000 + n)
			samples := ds.generate(rand.New(rand.NewPCG(seed, streamSeed)), n)
			sender := &replay{samples: samples}
			runner, err := load.NewRunner(load.Config{Pattern: load.WorkerPool, Concurrency: 1, Requests: int64(n)}, sender)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			started := time.Now()
			summary, err := runner.Run(context.Background())
			wall := time.Since(started)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			stats := summary.SuccessLatency
			probe := hdrhistogram.New(load.LatencyLowest, load.LatencyHighest, load.LatencySignificantDigits)
			var recordable []time.Duration
			exceeded := 0
			for _, d := range samples {
				if int64(d/load.LatencyUnit) > load.LatencyHighest {
					exceeded++
				}
				if probe.RecordValue(int64(d/load.LatencyUnit)) == nil {
					recordable = append(recordable, d)
				}
			}
			slices.Sort(recordable)
			m := len(recordable)
			var total time.Duration
			for _, d := range recordable {
				total += d
			}
			type stat struct {
				name string
				rank int
				ref  time.Duration
				ceil time.Duration
				hist time.Duration
			}
			stats3 := []stat{}
			for _, p := range []struct {
				name string
				q    float64
				hist time.Duration
			}{{"p50", 50, stats.P50}, {"p95", 95, stats.P95}, {"p99", 99, stats.P99}} {
				rank := hdrRank(p.q, m)
				stats3 = append(stats3, stat{p.name, rank, recordable[rank-1], recordable[ceilRank(p.q, m)-1], p.hist})
			}
			stats3 = append(stats3,
				stat{"min", 1, recordable[0], recordable[0], stats.Min},
				stat{"max", m, recordable[m-1], recordable[m-1], stats.Max},
				stat{"mean", 0, total / time.Duration(m), total / time.Duration(m), stats.Mean})
			for _, s := range stats3 {
				truncated := float64(s.ref / load.LatencyUnit)
				if s.name == "mean" {
					truncated = math.NaN()
				}
				absErr := math.Abs(us(s.hist) - us(s.ref))
				rel := math.NaN()
				if s.ref > 0 {
					rel = absErr / us(s.ref) * 100
				}
				quant := truncated - us(s.ref)
				histErr := us(s.hist) - truncated
				fmtNaN := func(v float64) string {
					if math.IsNaN(v) {
						return "NA"
					}
					return f(v)
				}
				rank := strconv.Itoa(s.rank)
				if s.name == "mean" {
					rank = "NA"
				}
				w.Write([]string{ds.name, strconv.Itoa(n), fmt.Sprintf("PCG(%d,%d)", seed, streamSeed), s.name, rank,
					f(us(s.ref)), fmtNaN(truncated), f(us(s.hist)), f(absErr), fmtNaN(rel), fmtNaN(quant), fmtNaN(histErr),
					f(us(s.ceil)), f(us(s.ceil) - us(s.ref)), strconv.FormatInt(stats.Count, 10),
					strconv.FormatInt(stats.Rejected, 10), strconv.Itoa(n - m), strconv.Itoa(m), strconv.Itoa(exceeded),
					load.LatencyUnit.String(), strconv.FormatInt(load.LatencyLowest, 10),
					strconv.FormatInt(load.LatencyHighest, 10), strconv.Itoa(load.LatencySignificantDigits),
					f(float64(wall) / float64(time.Millisecond))})
			}
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	file.Close()
}
