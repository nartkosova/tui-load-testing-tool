package load

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

const benchmarkRequests = 10_000

// spin busy-waits so the processing cost uses CPU like real result handling.
func spin(d time.Duration) {
	for start := time.Now(); time.Since(start) < d; {
	}
}

func benchmarkPattern(b *testing.B, pattern Pattern, concurrency int, sendCost, processCost time.Duration) {
	if processCost > 0 {
		b.Cleanup(func() { observe = classify })
		observe = func(result request.Result) observation {
			spin(processCost)
			return classify(result)
		}
	}
	sender := senderFunc(func(context.Context) request.Result {
		if sendCost > 0 {
			time.Sleep(sendCost)
		}
		return request.Result{StatusCode: 200, Duration: time.Millisecond}
	})
	runner, err := NewRunner(Config{Pattern: pattern, Concurrency: concurrency, Requests: benchmarkRequests}, sender)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := runner.Run(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*benchmarkRequests), "ns/request")
}

// BenchmarkPatternOverhead measures coordination cost with an instant sender.
func BenchmarkPatternOverhead(b *testing.B) {
	for _, concurrency := range []int{10, 100, 1000} {
		for _, pattern := range patterns {
			b.Run(fmt.Sprintf("%s/c=%d", pattern, concurrency), func(b *testing.B) {
				benchmarkPattern(b, pattern, concurrency, 0, 0)
			})
		}
	}
}

// BenchmarkPatternProcessingCost adds CPU work to result processing while
// requests take 1ms, exposing where processing runs in each pattern.
func BenchmarkPatternProcessingCost(b *testing.B) {
	for _, cost := range []time.Duration{0, 20 * time.Microsecond, 100 * time.Microsecond} {
		for _, pattern := range patterns {
			b.Run(fmt.Sprintf("%s/cost=%s", pattern, cost), func(b *testing.B) {
				benchmarkPattern(b, pattern, 100, time.Millisecond, cost)
			})
		}
	}
}
