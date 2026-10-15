package load

import (
	"context"

	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

func pipeline(ctx context.Context, sender Sender, cfg Config) <-chan observation {
	jobs := make(chan struct{}, cfg.Concurrency)
	raw := make(chan request.Result, cfg.Concurrency)
	results := make(chan observation, cfg.Concurrency)
	workers := startWorkers(ctx, sender, jobs, cfg.Concurrency, func(result request.Result) {
		raw <- result
	})
	go func() {
		produce(ctx, jobs, cfg.Requests)
		workers.Wait()
		close(raw)
	}()
	go func() {
		defer close(results)
		for result := range raw {
			results <- observe(result)
		}
	}()
	return results
}
