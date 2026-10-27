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
	"github.com/nartkosova/tui-load-testing-tool/internal/tui"
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
	if len(args) > 0 && args[0] == "monitor" {
		return runMonitor(ctx, args[1:], stdout, stderr)
	}
	var target, method, body, pattern string
	var timeout, duration time.Duration
	var concurrency int
	var interactive bool
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
	flags.BoolVar(&interactive, "tui", false, "open the interactive terminal UI (flags prefill the form)")
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

	if interactive {
		return runInteractive(ctx, flags, tui.Settings{
			URL: target, Method: method, Pattern: pattern, Concurrency: concurrency,
			Requests: requests, Duration: duration, Timeout: timeout,
		}, http.Header(headers), []byte(body), stdout, stderr)
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
		printSummary(stdout, pattern, concurrency, summary)
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

func runInteractive(ctx context.Context, flags *flag.FlagSet, initial tui.Settings, header http.Header, body []byte, stdout, stderr io.Writer) int {
	explicitRequests := false
	flags.Visit(func(f *flag.Flag) { explicitRequests = explicitRequests || f.Name == "requests" })
	if !explicitRequests {
		initial.Requests = 0
		if initial.Duration == 0 {
			initial.Duration = 10 * time.Second
		}
	}
	result, err := tui.Run(ctx, initial, func(s tui.Settings) (*load.Runner, error) {
		generator, err := request.NewGenerator(request.Config{
			URL: s.URL, Method: s.Method, Header: header, Body: body, Timeout: s.Timeout,
		})
		if err != nil {
			return nil, err
		}
		return load.NewRunner(load.Config{
			Pattern: load.Pattern(s.Pattern), Concurrency: s.Concurrency,
			Requests: s.Requests, Duration: s.Duration,
		}, generator)
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !result.Ran {
		return 0
	}
	fmt.Fprintf(stdout, "url: %s\n", result.Settings.URL)
	printSummary(stdout, result.Settings.Pattern, result.Settings.Concurrency, result.Summary)
	if result.Err != nil {
		fmt.Fprintf(stderr, "load test stopped: %v\n", result.Err)
		return 1
	}
	return 0
}

func printSummary(w io.Writer, pattern string, concurrency int, summary load.Summary) {
	fmt.Fprintf(w, "pattern: %s\nconcurrency: %d\nrequests: %d\nresponses: %d\nerrors: %d\nHTTP errors: %d\ncanceled: %d\nduration: %s\n",
		pattern, concurrency, summary.Requests, summary.Responses, summary.Errors, summary.HTTPErrors,
		summary.Canceled, summary.Duration)
	fmt.Fprintf(w, "requests/sec: %s\n", formatValue(summary.RequestRate()))
	fmt.Fprintf(w, "error rate: %s\n", formatPercent(summary.ErrorRate()))
	fmt.Fprintf(w, "HTTP error rate: %s\n", formatPercent(summary.HTTPErrorRate()))
	fmt.Fprintf(w, "response bytes: %d\n", summary.BytesRead)
	printLatency(w, "success latency", summary.SuccessLatency)
	printLatency(w, "HTTP error latency", summary.HTTPErrorLatency)
	printLatency(w, "failure latency", summary.FailureLatency)
}

func printLatency(w io.Writer, name string, stats load.LatencyStats) {
	if !stats.Available() {
		fmt.Fprintf(w, "%s: unavailable (samples: 0, rejected: %d)\n", name, stats.Rejected)
		return
	}
	fmt.Fprintf(w, "%s: samples=%d rejected=%d min=%s avg=%s p50=%s p95=%s p99=%s max=%s\n",
		name, stats.Count, stats.Rejected, stats.Min, stats.Mean, stats.P50, stats.P95, stats.P99, stats.Max)
}

func formatValue(value float64, ok bool) string {
	if !ok {
		return "unavailable"
	}
	return fmt.Sprintf("%.2f", value)
}

func formatPercent(value float64, ok bool) string {
	if !ok {
		return "unavailable"
	}
	return fmt.Sprintf("%.2f%%", value)
}
