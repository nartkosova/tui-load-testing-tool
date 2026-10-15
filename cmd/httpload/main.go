package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nartkosova/tui-load-testing-tool/internal/load"
	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

type headersFlag http.Header

func (h *headersFlag) String() string {
	return ""
}

func (h *headersFlag) Set(value string) error {
	name, content, ok := strings.Cut(value, ":")
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("header must be in Name: value format")
	}
	http.Header(*h).Add(strings.TrimSpace(name), strings.TrimSpace(content))
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var target, method, body, pattern string
	var timeout, duration time.Duration
	var concurrency int
	var requests int64
	var headers = make(headersFlag)
	flags := flag.NewFlagSet("httpload", flag.ContinueOnError)
	flags.SetOutput(stderr)

	flags.StringVar(&target, "url", "", "absolute HTTP or HTTPS target URL")
	flags.StringVar(&method, "method", http.MethodGet, "HTTP method")
	flags.StringVar(&body, "body", "", "request body")
	flags.DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	flags.Var(&headers, "header", "request header in 'Name: value' format (repeatable)")
	flags.StringVar(&pattern, "pattern", string(load.WorkerPool), "concurrency pattern: worker-pool, fan-out-fan-in, pipeline")
	flags.IntVar(&concurrency, "concurrency", 1, "maximum simultaneous requests")
	flags.Int64Var(&requests, "requests", 1, "request count (0 requires a duration)")
	flags.DurationVar(&duration, "duration", 0, "load test duration (also limits an explicit request count)")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected positional arguments")
		return 2
	}

	if target == "" {
		fmt.Fprintln(stderr, "-url is required")
		flags.Usage()
		return 2
	}

	generator, err := request.NewGenerator(request.Config{
		URL:     target,
		Method:  method,
		Header:  http.Header(headers),
		Body:    []byte(body),
		Timeout: timeout,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	var loadMode, explicitRequests bool
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "pattern", "concurrency", "requests", "duration":
			loadMode = true
		}
		if f.Name == "requests" {
			explicitRequests = true
		}
	})
	if loadMode {
		if duration > 0 && !explicitRequests {
			requests = 0
		}
		runner, err := load.NewRunner(load.Config{
			Pattern: load.Pattern(pattern), Concurrency: concurrency,
			Requests: requests, Duration: duration,
		}, generator)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		summary, err := runner.Run(ctx)
		fmt.Fprintf(stdout, "pattern: %s\nconcurrency: %d\nrequests: %d\nresponses: %d\nerrors: %d\nHTTP errors: %d\ncanceled: %d\nduration: %s\nrequests/sec: %.2f\nresponse bytes: %d\n",
			pattern, concurrency, summary.Requests, summary.Responses, summary.Errors, summary.HTTPErrors,
			summary.Canceled, summary.Duration, float64(summary.Requests)/summary.Duration.Seconds(), summary.BytesRead)
		if err != nil {
			fmt.Fprintf(stderr, "load test stopped: %v\n", err)
			return 1
		}
		return 0
	}

	result := generator.Send(ctx)
	if result.Err != nil {
		fmt.Fprintf(stderr, "error: %s: %v\n", result.ErrorKind, result.Err)
		return 1
	}
	fmt.Fprintf(stdout, "status: %d\nduration: %s\nresponse bytes: %d\n", result.StatusCode, result.Duration, result.BytesRead)
	return 0
}
