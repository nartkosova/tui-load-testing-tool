package load

import (
	"context"

	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

func workerPool(ctx context.Context, sender Sender, cfg Config) <-chan observation {
	jobs := make(chan struct{}, cfg.Concurrency)
	results := make(chan observation, cfg.Concurrency)
	workers := startWorkers(ctx, sender, jobs, cfg.Concurrency, func(result request.Result) {
		results <- observe(result)
	})
	go func() {
		produce(ctx, jobs, cfg.Requests)
		workers.Wait()
		close(results)
	}()
	return results
}
