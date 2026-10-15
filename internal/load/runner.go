package load

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

type Pattern string

const (
	WorkerPool  Pattern = "worker-pool"
	FanOutFanIn Pattern = "fan-out-fan-in"
	Pipeline    Pattern = "pipeline"
)

type Config struct {
	Pattern     Pattern
	Concurrency int
	Requests    int64
	Duration    time.Duration
}

type Sender interface {
	// Send must support concurrent calls and honor context cancellation.
	Send(context.Context) request.Result
}

type Summary struct {
	Requests   int64
	Responses  int64
	Errors     int64
	HTTPErrors int64
	Canceled   int64
	BytesRead  int64
	Duration   time.Duration
}

type observation struct {
	result    request.Result
	httpError bool
}

type strategy func(context.Context, Sender, Config) <-chan observation

type Runner struct {
	cfg     Config
	sender  Sender
	execute strategy
}

func NewRunner(cfg Config, sender Sender) (*Runner, error) {
	if cfg.Concurrency <= 0 {
		return nil, errors.New("concurrency must be greater than zero")
	}
	if cfg.Requests < 0 || cfg.Duration < 0 {
		return nil, errors.New("requests and duration must not be negative")
	}
	if cfg.Requests == 0 && cfg.Duration == 0 {
		return nil, errors.New("a positive request count or duration is required")
	}
	if sender == nil {
		return nil, errors.New("request sender is required")
	}
	var execute strategy
	switch cfg.Pattern {
	case WorkerPool:
		execute = workerPool
	case FanOutFanIn:
		execute = fanOutFanIn
	case Pipeline:
		execute = pipeline
	default:
		return nil, fmt.Errorf("unknown concurrency pattern %q", cfg.Pattern)
	}
	return &Runner{cfg: cfg, sender: sender, execute: execute}, nil
}

func (r *Runner) Run(ctx context.Context) (Summary, error) {
	start := time.Now()
	runCtx := ctx
	if r.cfg.Duration > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, r.cfg.Duration)
		defer cancel()
	}

	var summary Summary
	// Drain every started request, including failures returned during cancellation.
	for item := range r.execute(runCtx, r.sender, r.cfg) {
		summary.Requests++
		summary.BytesRead += item.result.BytesRead
		if item.result.Err != nil {
			summary.Errors++
		} else {
			summary.Responses++
		}
		if item.httpError {
			summary.HTTPErrors++
		}
		if item.result.ErrorKind == request.ErrorCanceled {
			summary.Canceled++
		}
	}
	summary.Duration = time.Since(start)
	return summary, ctx.Err()
}

func observe(result request.Result) observation {
	return observation{result: result, httpError: result.StatusCode >= 400}
}

func produce(ctx context.Context, jobs chan<- struct{}, requests int64) {
	defer close(jobs)
	for i := int64(0); requests == 0 || i < requests; i++ {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case jobs <- struct{}{}:
		}
	}
}

func executeRequests(ctx context.Context, sender Sender, jobs <-chan struct{}, emit func(request.Result)) {
	for range jobs {
		if ctx.Err() != nil {
			return
		}
		emit(sender.Send(ctx))
	}
}

func startWorkers(ctx context.Context, sender Sender, jobs <-chan struct{}, concurrency int, emit func(request.Result)) *sync.WaitGroup {
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			executeRequests(ctx, sender, jobs, emit)
		}()
	}
	return &workers
}
