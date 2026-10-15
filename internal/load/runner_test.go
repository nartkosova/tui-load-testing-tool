package load

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

var patterns = []Pattern{WorkerPool, FanOutFanIn, Pipeline}

type senderFunc func(context.Context) request.Result

func (f senderFunc) Send(ctx context.Context) request.Result { return f(ctx) }

type runResult struct {
	summary Summary
	err     error
}

func runAsync(ctx context.Context, runner *Runner) <-chan runResult {
	done := make(chan runResult, 1)
	go func() {
		summary, err := runner.Run(ctx)
		done <- runResult{summary, err}
	}()
	return done
}

func awaitRun(t *testing.T, done <-chan runResult) runResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not stop")
		return runResult{}
	}
}

func awaitStart(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
}

func newTestRunner(t *testing.T, cfg Config, sender Sender) *Runner {
	t.Helper()
	runner, err := NewRunner(cfg, sender)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestPatternsRespectConcurrencyAndRequestCount(t *testing.T) {
	for _, pattern := range patterns {
		t.Run(string(pattern), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var started, active, peak atomic.Int64
			entered := make(chan struct{}, 17)
			release := make(chan struct{})
			sender := senderFunc(func(ctx context.Context) request.Result {
				started.Add(1)
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
				}
				entered <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
				}
				return request.Result{StatusCode: 200, BytesRead: 2}
			})
			runner := newTestRunner(t, Config{Pattern: pattern, Concurrency: 3, Requests: 17}, sender)
			done := runAsync(ctx, runner)
			for range 3 {
				awaitStart(t, entered)
			}
			if active.Load() != 3 {
				t.Errorf("expected three simultaneous requests, got %d", active.Load())
			}
			close(release)
			result := awaitRun(t, done)
			if result.err != nil || result.summary.Requests != 17 || result.summary.Responses != 17 || result.summary.BytesRead != 34 {
				t.Fatalf("unexpected run: %+v, %v", result.summary, result.err)
			}
			if started.Load() != 17 || active.Load() != 0 || peak.Load() != 3 {
				t.Fatalf("started=%d active=%d peak=%d", started.Load(), active.Load(), peak.Load())
			}
		})
	}
}

func TestPatternsAggregateResultsAndCanRunAgain(t *testing.T) {
	for _, pattern := range patterns {
		t.Run(string(pattern), func(t *testing.T) {
			var calls atomic.Int64
			sender := senderFunc(func(context.Context) request.Result {
				switch calls.Add(1) % 4 {
				case 0:
					return request.Result{StatusCode: 200, BytesRead: 3}
				case 1:
					return request.Result{StatusCode: 503, BytesRead: 4}
				case 2:
					return request.Result{ErrorKind: request.ErrorNetwork, Err: errors.New("network failure")}
				default:
					return request.Result{StatusCode: 500, BytesRead: 2, ErrorKind: request.ErrorResponseBody, Err: io.ErrUnexpectedEOF}
				}
			})
			runner := newTestRunner(t, Config{Pattern: pattern, Concurrency: 5, Requests: 20}, sender)
			for range 2 {
				result := awaitRun(t, runAsync(context.Background(), runner))
				summary := result.summary
				if result.err != nil || summary.Requests != 20 || summary.Responses != 10 || summary.Errors != 10 || summary.HTTPErrors != 10 || summary.BytesRead != 45 || summary.Canceled != 0 || summary.Duration <= 0 {
					t.Fatalf("unexpected run: %+v, %v", summary, result.err)
				}
			}
		})
	}
}

func TestPatternsCancelInFlightRequestsAndStopScheduling(t *testing.T) {
	for _, pattern := range patterns {
		t.Run(string(pattern), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{}, 4)
			var active atomic.Int64
			sender := senderFunc(func(ctx context.Context) request.Result {
				active.Add(1)
				defer active.Add(-1)
				entered <- struct{}{}
				<-ctx.Done()
				return request.Result{ErrorKind: request.ErrorCanceled, Err: ctx.Err()}
			})
			runner := newTestRunner(t, Config{Pattern: pattern, Concurrency: 4, Requests: 1_000_000}, sender)
			done := runAsync(ctx, runner)
			for range 4 {
				awaitStart(t, entered)
			}
			cancel()
			result := awaitRun(t, done)
			if !errors.Is(result.err, context.Canceled) || result.summary.Requests != 4 || result.summary.Errors != 4 || result.summary.Canceled != 4 || active.Load() != 0 {
				t.Fatalf("unexpected canceled run: %+v, %v, active=%d", result.summary, result.err, active.Load())
			}
		})
	}
}

func TestPatternsDoNotStartWithCanceledContext(t *testing.T) {
	for _, pattern := range patterns {
		t.Run(string(pattern), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var calls atomic.Int64
			runner := newTestRunner(t, Config{Pattern: pattern, Concurrency: 4, Requests: 20}, senderFunc(func(context.Context) request.Result {
				calls.Add(1)
				return request.Result{}
			}))
			result := awaitRun(t, runAsync(ctx, runner))
			if !errors.Is(result.err, context.Canceled) || result.summary.Requests != 0 || calls.Load() != 0 {
				t.Fatalf("unexpected run: %+v, %v, calls=%d", result.summary, result.err, calls.Load())
			}
		})
	}
}

func TestPatternsStopAtDuration(t *testing.T) {
	for _, pattern := range patterns {
		t.Run(string(pattern), func(t *testing.T) {
			var calls, active atomic.Int64
			sender := senderFunc(func(ctx context.Context) request.Result {
				calls.Add(1)
				active.Add(1)
				defer active.Add(-1)
				<-ctx.Done()
				return request.Result{ErrorKind: request.ErrorTimeout, Err: ctx.Err()}
			})
			runner := newTestRunner(t, Config{Pattern: pattern, Concurrency: 3, Duration: 20 * time.Millisecond}, sender)
			result := awaitRun(t, runAsync(context.Background(), runner))
			if result.err != nil || result.summary.Requests != calls.Load() || result.summary.Errors != calls.Load() || active.Load() != 0 || result.summary.Duration < 20*time.Millisecond {
				t.Fatalf("unexpected duration run: %+v, %v, calls=%d active=%d", result.summary, result.err, calls.Load(), active.Load())
			}
		})
	}
}

func TestPatternsRequestLimitCanFinishBeforeDuration(t *testing.T) {
	for _, pattern := range patterns {
		t.Run(string(pattern), func(t *testing.T) {
			runner := newTestRunner(t, Config{Pattern: pattern, Concurrency: 8, Requests: 1, Duration: time.Hour}, senderFunc(func(context.Context) request.Result {
				return request.Result{StatusCode: 200}
			}))
			result := awaitRun(t, runAsync(context.Background(), runner))
			if result.err != nil || result.summary.Requests != 1 {
				t.Fatalf("unexpected run: %+v, %v", result.summary, result.err)
			}
		})
	}
}

func TestPatternsDrainResultsAfterBackpressureAndCancellation(t *testing.T) {
	for _, pattern := range patterns {
		t.Run(string(pattern), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int64
			entered := make(chan struct{}, 1)
			runner := newTestRunner(t, Config{Pattern: pattern, Concurrency: 1, Requests: 100}, senderFunc(func(context.Context) request.Result {
				if calls.Add(1) == 2 {
					entered <- struct{}{}
				}
				return request.Result{StatusCode: 200}
			}))
			output := runner.execute(ctx, runner.sender, runner.cfg)
			awaitStart(t, entered)
			cancel()
			done := make(chan runResult, 1)
			go func() {
				var summary Summary
				for range output {
					summary.Requests++
				}
				done <- runResult{summary: summary}
			}()
			result := awaitRun(t, done)
			if result.summary.Requests != calls.Load() || calls.Load() < 2 {
				t.Fatalf("lost results: collected=%d sent=%d", result.summary.Requests, calls.Load())
			}
		})
	}
}

func TestPatternsUseHTTPGenerator(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "payload" || r.Method != http.MethodPost || r.Header.Get("X-Test") != "shared" {
			t.Errorf("unexpected request: method=%s header=%s body=%s err=%v", r.Method, r.Header.Get("X-Test"), body, err)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "busy")
	}))
	defer server.Close()
	for _, pattern := range patterns {
		t.Run(string(pattern), func(t *testing.T) {
			generator, err := request.NewGenerator(request.Config{
				URL: server.URL, Method: http.MethodPost, Header: http.Header{"X-Test": {"shared"}},
				Body: []byte("payload"), Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			runner := newTestRunner(t, Config{Pattern: pattern, Concurrency: 4, Requests: 25}, generator)
			result := awaitRun(t, runAsync(context.Background(), runner))
			if result.err != nil || result.summary.Responses != 25 || result.summary.HTTPErrors != 25 || result.summary.Errors != 0 || result.summary.BytesRead != 100 {
				t.Fatalf("unexpected HTTP run: %+v, %v", result.summary, result.err)
			}
		})
	}
}

func TestNewRunnerRejectsInvalidConfiguration(t *testing.T) {
	sender := senderFunc(func(context.Context) request.Result { return request.Result{} })
	tests := []Config{
		{Pattern: "unknown", Concurrency: 1, Requests: 1},
		{Pattern: WorkerPool, Concurrency: 0, Requests: 1},
		{Pattern: WorkerPool, Concurrency: -1, Requests: 1},
		{Pattern: WorkerPool, Concurrency: 1, Requests: -1},
		{Pattern: WorkerPool, Concurrency: 1, Requests: 1, Duration: -time.Second},
		{Pattern: WorkerPool, Concurrency: 1},
	}
	for _, cfg := range tests {
		if _, err := NewRunner(cfg, sender); err == nil {
			t.Errorf("accepted invalid configuration: %+v", cfg)
		}
	}
	if _, err := NewRunner(Config{Pattern: WorkerPool, Concurrency: 1, Requests: 1}, nil); err == nil {
		t.Error("accepted nil sender")
	}
}
