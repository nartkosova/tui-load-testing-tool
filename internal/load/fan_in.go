package load

import (
	"context"
	"sync"

	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

func fanOutFanIn(ctx context.Context, sender Sender, cfg Config) <-chan observation {
	jobs := make(chan struct{}, cfg.Concurrency)
	results := make(chan observation, cfg.Concurrency)
	var mergers sync.WaitGroup
	for range cfg.Concurrency {
		output := make(chan observation)
		go func() {
			defer close(output)
			executeRequests(ctx, sender, jobs, func(result request.Result) {
				output <- observe(result)
			})
		}()
		mergers.Add(1)
		go func() {
			defer mergers.Done()
			for result := range output {
				results <- result
			}
		}()
	}
	go func() {
		produce(ctx, jobs, cfg.Requests)
		mergers.Wait()
		close(results)
	}()
	return results
}
