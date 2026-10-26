package load

import (
	"context"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

func collect(results ...request.Result) *collector {
	c := newCollector()
	for _, result := range results {
		c.record(observe(result))
	}
	return c
}

func TestCollectorCountsResultCategories(t *testing.T) {
	c := collect(
		request.Result{StatusCode: 200, BytesRead: 5, Duration: time.Millisecond},
		request.Result{StatusCode: 404, BytesRead: 3, Duration: 2 * time.Millisecond},
		request.Result{ErrorKind: request.ErrorNetwork, Err: errors.New("refused"), Duration: 3 * time.Millisecond},
		request.Result{StatusCode: 500, BytesRead: 1, ErrorKind: request.ErrorResponseBody, Err: io.ErrUnexpectedEOF, Duration: 4 * time.Millisecond},
		request.Result{ErrorKind: request.ErrorCanceled, Err: context.Canceled, Duration: 5 * time.Millisecond},
	)
	s := c.result(time.Second)
	if s.Requests != 5 || s.Responses != 2 || s.Errors != 3 || s.HTTPErrors != 2 || s.Canceled != 1 || s.BytesRead != 9 || s.Duration != time.Second {
		t.Fatalf("unexpected counters: %+v", s)
	}
	if s.SuccessLatency.Count != 1 || s.HTTPErrorLatency.Count != 1 || s.FailureLatency.Count != 3 {
		t.Fatalf("unexpected distribution sizes: success=%d http=%d failure=%d",
			s.SuccessLatency.Count, s.HTTPErrorLatency.Count, s.FailureLatency.Count)
	}
}

func TestSummaryRates(t *testing.T) {
	s := Summary{Requests: 1000, Errors: 50, HTTPErrors: 125, Duration: 2 * time.Second}
	if rate, ok := s.RequestRate(); !ok || rate != 500 {
		t.Errorf("request rate = %v, %v", rate, ok)
	}
	if rate, ok := s.ErrorRate(); !ok || rate != 5 {
		t.Errorf("error rate = %v, %v", rate, ok)
	}
	if rate, ok := s.HTTPErrorRate(); !ok || rate != 12.5 {
		t.Errorf("HTTP error rate = %v, %v", rate, ok)
	}
}

func TestSummaryRatesUnavailableWithoutResults(t *testing.T) {
	for _, s := range []Summary{{}, {Duration: time.Second}, {Requests: 1}} {
		if _, ok := s.RequestRate(); ok {
			t.Errorf("request rate available for %+v", s)
		}
	}
	s := Summary{Duration: time.Second}
	if _, ok := s.ErrorRate(); ok {
		t.Error("error rate available without requests")
	}
	if _, ok := s.HTTPErrorRate(); ok {
		t.Error("HTTP error rate available without requests")
	}
}

func TestLatencyEmptyDistributionIsUnavailable(t *testing.T) {
	stats := newLatencyRecorder().stats()
	if stats.Available() || stats.P50 != 0 || stats.Mean != 0 {
		t.Fatalf("empty distribution reported a measurement: %+v", stats)
	}
}

func TestLatencySingleObservation(t *testing.T) {
	r := newLatencyRecorder()
	r.record(1500 * time.Microsecond)
	stats := r.stats()
	for name, got := range map[string]time.Duration{"min": stats.Min, "mean": stats.Mean, "p50": stats.P50, "p99": stats.P99, "max": stats.Max} {
		assertClose(t, name, 1500*time.Microsecond, got)
	}
}

func TestLatencyRejectsValuesOutsideRange(t *testing.T) {
	r := newLatencyRecorder()
	highest := time.Duration(LatencyHighest) * LatencyUnit
	r.record(time.Duration(LatencyLowest) * LatencyUnit)
	r.record(highest)
	r.record(highest + time.Hour)
	r.record(-time.Millisecond)
	stats := r.stats()
	if stats.Count != 2 || stats.Rejected != 2 {
		t.Fatalf("count=%d rejected=%d", stats.Count, stats.Rejected)
	}
	assertClose(t, "max", highest, stats.Max)
}

func TestLatencyPercentilesMatchReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	datasets := map[string][]time.Duration{
		"repeated": slices.Repeat([]time.Duration{2 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond}, 200),
		"wide range": func() []time.Duration {
			var values []time.Duration
			for range 5000 {
				// Log-uniform between 10µs and 10s.
				values = append(values, time.Duration(math.Pow(10, 1+6*rng.Float64()))*time.Microsecond)
			}
			return values
		}(),
		"slow tail": func() []time.Duration {
			var values []time.Duration
			for range 990 {
				values = append(values, time.Duration(20+rng.IntN(10))*time.Millisecond)
			}
			for range 10 {
				values = append(values, time.Duration(2+rng.IntN(3))*time.Second)
			}
			return values
		}(),
	}
	for name, samples := range datasets {
		t.Run(name, func(t *testing.T) {
			r := newLatencyRecorder()
			var total time.Duration
			for _, d := range samples {
				r.record(d)
				total += d
			}
			stats := r.stats()
			sorted := slices.Sorted(slices.Values(samples))
			if stats.Count != int64(len(samples)) || stats.Rejected != 0 {
				t.Fatalf("count=%d rejected=%d", stats.Count, stats.Rejected)
			}
			assertClose(t, "p50", referencePercentile(sorted, 50), stats.P50)
			assertClose(t, "p95", referencePercentile(sorted, 95), stats.P95)
			assertClose(t, "p99", referencePercentile(sorted, 99), stats.P99)
			assertClose(t, "min", sorted[0], stats.Min)
			assertClose(t, "max", sorted[len(sorted)-1], stats.Max)
			assertClose(t, "mean", total/time.Duration(len(samples)), stats.Mean)
		})
	}
}

// referencePercentile uses the rank convention of hdrhistogram-go v1.4.0:
// rank = round(p/100 * n), clamped to [1, n], on the sorted samples.
func referencePercentile(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	rank := int(p/100*float64(n) + 0.5)
	rank = max(1, min(rank, n))
	return sorted[rank-1]
}

// assertClose allows the relative error implied by the configured significant
// digits, plus one recording unit lost to truncation.
func assertClose(t *testing.T, name string, want, got time.Duration) {
	t.Helper()
	diff := math.Abs(float64(got - want))
	tolerance := float64(want)*math.Pow(10, -LatencySignificantDigits) + float64(LatencyUnit)
	if diff > tolerance {
		t.Errorf("%s: want %s got %s (absolute error %s)", name, want, got, time.Duration(diff))
		return
	}
	if want > 0 {
		t.Logf("%s: want %s got %s absolute error %s relative error %.5f%%", name, want, got, time.Duration(diff), diff/float64(want)*100)
	}
}

func benchmarkResults() []observation {
	rng := rand.New(rand.NewPCG(3, 4))
	items := make([]observation, 4096)
	for i := range items {
		items[i] = observe(request.Result{StatusCode: 200, BytesRead: 512, Duration: time.Duration(1+rng.IntN(500_000)) * time.Microsecond})
	}
	return items
}

func BenchmarkCollectorCountersOnly(b *testing.B) {
	items := benchmarkResults()
	var s Summary
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		item := items[i%len(items)]
		s.Requests++
		s.BytesRead += item.result.BytesRead
		if item.result.Err != nil {
			s.Errors++
		} else {
			s.Responses++
		}
		if item.httpError {
			s.HTTPErrors++
		}
	}
}

func BenchmarkCollectorWithLatency(b *testing.B) {
	items := benchmarkResults()
	c := newCollector()
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		c.record(items[i%len(items)])
	}
}

func BenchmarkCollectorConstruction(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = newCollector()
	}
}

func BenchmarkCollectorSummary(b *testing.B) {
	c := newCollector()
	for _, item := range benchmarkResults() {
		c.record(item)
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = c.result(time.Second)
	}
}
