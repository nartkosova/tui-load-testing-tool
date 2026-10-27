package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nartkosova/tui-load-testing-tool/internal/monitor"
	"github.com/nartkosova/tui-load-testing-tool/internal/tui"
)

type urlsFlag []string

func (u *urlsFlag) String() string {
	return strings.Join(*u, ",")
}

func (u *urlsFlag) Set(value string) error {
	*u = append(*u, value)
	return nil
}

const timeFormat = "2006-01-02 15:04:05"

func runMonitor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var configPath, method string
	var interval, timeout, degraded time.Duration
	var failThreshold int
	var interactive, verbose bool
	var urls urlsFlag
	var headers = make(headersFlag)
	flags := flag.NewFlagSet("httpload monitor", flag.ContinueOnError)
	flags.SetOutput(stderr)

	flags.StringVar(&configPath, "config", "", "JSON file with targets")
	flags.Var(&urls, "url", "target URL to monitor (repeatable)")
	flags.StringVar(&method, "method", http.MethodGet, "HTTP method")
	flags.Var(&headers, "header", "request header in 'Name: value' format (repeatable)")
	flags.DurationVar(&interval, "interval", monitor.DefaultInterval, "time between checks of a target")
	flags.DurationVar(&timeout, "timeout", monitor.DefaultTimeout, "check timeout (at most the interval)")
	flags.IntVar(&failThreshold, "fail-threshold", monitor.DefaultFailThreshold, "consecutive failed checks before a target is down")
	flags.DurationVar(&degraded, "degraded", 0, "latency above which a successful check is degraded (0 disables)")
	flags.BoolVar(&interactive, "tui", false, "show the live monitor in the terminal UI")
	flags.BoolVar(&verbose, "v", false, "log every check, not only state changes")
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

	defaults := monitor.TargetConfig{
		Method: method, Interval: monitor.Duration(interval), Timeout: monitor.Duration(min(timeout, interval)),
		FailThreshold: failThreshold, Degraded: monitor.Duration(degraded), Headers: map[string]string{},
	}
	for name, values := range headers {
		defaults.Headers[name] = strings.Join(values, ", ")
	}
	var configs []monitor.TargetConfig
	if configPath != "" {
		file, err := os.Open(configPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		configs, err = monitor.LoadConfig(file)
		file.Close()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	for _, u := range urls {
		configs = append(configs, monitor.TargetConfig{URL: u})
	}
	if len(configs) == 0 {
		fmt.Fprintln(stderr, "-url or -config is required")
		flags.Usage()
		return 2
	}

	targets := make([]monitor.Target, len(configs))
	for i, c := range configs {
		target, err := c.WithDefaults(defaults).Target()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		targets[i] = target
	}
	mon, err := monitor.New(targets)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	var statuses []monitor.Status
	if interactive {
		statuses, err = tui.RunMonitor(ctx, mon)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		fmt.Fprintf(stdout, "monitoring %d targets, press Ctrl+C to stop\n", len(targets))
		statuses = mon.Run(ctx, func(u monitor.Update) { logUpdate(stdout, u, verbose) })
	}
	printMonitorSummary(stdout, statuses, time.Now())
	return 0
}

func logUpdate(w io.Writer, u monitor.Update, verbose bool) {
	s := u.Status
	result := fmt.Sprintf("HTTP %d", u.Check.Result.StatusCode)
	if !u.Check.OK {
		result = monitor.Cause(u.Check.Result)
	}
	switch {
	case u.Changed() && s.State == monitor.Down:
		fmt.Fprintf(w, "%s  %s  %s → DOWN since %s  %s\n", u.Check.At.Format(timeFormat), s.Name, u.Previous,
			s.Since.Format(timeFormat), result)
	case u.Changed() && u.Previous == monitor.Down:
		incident := s.Incidents[len(s.Incidents)-1]
		fmt.Fprintf(w, "%s  %s  DOWN → %s after %s  %s in %s\n", u.Check.At.Format(timeFormat), s.Name, s.State,
			incident.Duration(u.Check.At).Round(time.Second), result, u.Check.Result.Duration.Round(time.Millisecond))
	case u.Changed() || verbose:
		state := s.State.String()
		if u.Changed() {
			state = u.Previous.String() + " → " + state
		}
		fmt.Fprintf(w, "%s  %s  %s  %s in %s\n", u.Check.At.Format(timeFormat), s.Name, state, result,
			u.Check.Result.Duration.Round(time.Millisecond))
	}
}

func printMonitorSummary(w io.Writer, statuses []monitor.Status, now time.Time) {
	for _, s := range statuses {
		fmt.Fprintf(w, "\n%s\nstate: %s\nchecks: %d\nfailed checks: %d\nincidents: %d\n",
			s.Name, s.State, s.Checks, s.Failures, len(s.Incidents))
		if s.Total.Observed > 0 {
			fmt.Fprintf(w, "uptime: %.3f%% over %s\n", s.Total.Percent, s.Total.Observed.Round(time.Second))
		}
		if s.Drift > 0 {
			fmt.Fprintf(w, "latency: recent %s baseline %s drift %.2fx\n",
				s.Recent.Round(time.Microsecond), s.Baseline.Round(time.Microsecond), s.Drift)
		}
		for _, i := range s.Incidents {
			end := "ongoing"
			if !i.End.IsZero() {
				end = i.End.Format(timeFormat)
			}
			fmt.Fprintf(w, "  down %s → %s (%s) %s\n", i.Start.Format(timeFormat), end,
				i.Duration(now).Round(time.Second), i.Cause)
		}
	}
}
