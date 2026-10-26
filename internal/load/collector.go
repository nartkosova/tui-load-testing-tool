package load

import (
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"

	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

// Latency histogram configuration. Values are recorded in microseconds, so the
// trackable range is 1µs to 10 minutes with three significant digits (relative
// error below 0.1%). These values belong in every benchmark configuration.
const (
	LatencyUnit              = time.Microsecond
	LatencyLowest            = int64(1)
	LatencyHighest           = int64(10 * time.Minute / LatencyUnit)
	LatencySignificantDigits = 3
	LatencyBuckets           = 16
)

// LatencyStats summarizes one latency distribution. When Count is zero the
// distribution is unavailable and the remaining fields carry no measurement.
type LatencyStats struct {
	Count    int64
	Rejected int64
	Min      time.Duration
	Mean     time.Duration
	P50      time.Duration
	P95      time.Duration
	P99      time.Duration
	Max      time.Duration
	// Buckets splits Min..P99 into LatencyBuckets equal ranges for display.
	// Samples above P99 are counted in the last bucket.
	Buckets []LatencyBucket
}

type LatencyBucket struct {
	From  time.Duration
	To    time.Duration
	Count int64
}

func (s LatencyStats) Available() bool {
	return s.Count > 0
}

// latencyRecorder owns one histogram. It is only used by the collector
// goroutine, since the HDR histogram provides no internal synchronization.
type latencyRecorder struct {
	histogram *hdrhistogram.Histogram
	rejected  int64
}

func newLatencyRecorder() *latencyRecorder {
	return &latencyRecorder{histogram: hdrhistogram.New(LatencyLowest, LatencyHighest, LatencySignificantDigits)}
}

func (r *latencyRecorder) record(d time.Duration) {
	if err := r.histogram.RecordValue(int64(d / LatencyUnit)); err != nil {
		r.rejected++
	}
}

func (r *latencyRecorder) stats() LatencyStats {
	h := r.histogram
	stats := LatencyStats{Count: h.TotalCount(), Rejected: r.rejected}
	if stats.Count == 0 {
		return stats
	}
	value := func(v int64) time.Duration { return time.Duration(v) * LatencyUnit }
	stats.Min = value(h.Min())
	stats.Mean = time.Duration(h.Mean() * float64(LatencyUnit))
	stats.P50 = value(h.ValueAtQuantile(50))
	stats.P95 = value(h.ValueAtQuantile(95))
	stats.P99 = value(h.ValueAtQuantile(99))
	stats.Max = value(h.Max())
	stats.Buckets = buckets(h, stats.Min, stats.P99)
	return stats
}

func buckets(h *hdrhistogram.Histogram, low, high time.Duration) []LatencyBucket {
	width := max((high-low)/LatencyBuckets, LatencyUnit)
	result := make([]LatencyBucket, LatencyBuckets)
	for i := range result {
		result[i].From = low + time.Duration(i)*width
		result[i].To = result[i].From + width
	}
	for _, bar := range h.Distribution() {
		if bar.Count == 0 {
			continue
		}
		i := int((time.Duration(bar.From)*LatencyUnit - low) / width)
		result[max(0, min(i, LatencyBuckets-1))].Count += bar.Count
	}
	return result
}

// collector aggregates the observations delivered by every concurrency pattern.
type collector struct {
	summary   Summary
	success   *latencyRecorder
	httpError *latencyRecorder
	failure   *latencyRecorder
}

func newCollector() *collector {
	return &collector{
		success:   newLatencyRecorder(),
		httpError: newLatencyRecorder(),
		failure:   newLatencyRecorder(),
	}
}

func (c *collector) record(item observation) {
	c.summary.Requests++
	c.summary.BytesRead += item.result.BytesRead
	if item.result.Err != nil {
		c.summary.Errors++
	} else {
		c.summary.Responses++
	}
	if item.httpError {
		c.summary.HTTPErrors++
	}
	if item.result.ErrorKind == request.ErrorCanceled {
		c.summary.Canceled++
	}
	switch {
	case item.result.Err != nil:
		c.failure.record(item.result.Duration)
	case item.httpError:
		c.httpError.record(item.result.Duration)
	default:
		c.success.record(item.result.Duration)
	}
}

func (c *collector) result(duration time.Duration) Summary {
	summary := c.summary
	summary.Duration = duration
	summary.SuccessLatency = c.success.stats()
	summary.HTTPErrorLatency = c.httpError.stats()
	summary.FailureLatency = c.failure.stats()
	return summary
}

// RequestRate returns attempted requests per second, including attempts that
// ended in execution errors. It reports false when no rate can be calculated.
func (s Summary) RequestRate() (float64, bool) {
	if s.Requests == 0 || s.Duration <= 0 {
		return 0, false
	}
	return float64(s.Requests) / s.Duration.Seconds(), true
}

// ErrorRate returns the percentage of requests that ended in execution errors.
func (s Summary) ErrorRate() (float64, bool) {
	return s.percentage(s.Errors)
}

// HTTPErrorRate returns the percentage of requests with status 400 or higher.
func (s Summary) HTTPErrorRate() (float64, bool) {
	return s.percentage(s.HTTPErrors)
}

func (s Summary) percentage(count int64) (float64, bool) {
	if s.Requests == 0 {
		return 0, false
	}
	return float64(count) / float64(s.Requests) * 100, true
}
