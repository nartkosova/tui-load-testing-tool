package monitor

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

type senderFunc func(context.Context) request.Result

func (f senderFunc) Send(ctx context.Context) request.Result { return f(ctx) }

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func at(seconds int) time.Time { return epoch.Add(time.Duration(seconds) * time.Second) }

func ok(latency time.Duration) request.Result {
	return request.Result{StatusCode: 200, Duration: latency}
}

func fail() request.Result {
	return request.Result{ErrorKind: request.ErrorNetwork, Err: errors.New("refused"), Duration: time.Millisecond}
}

func feed(tr *tracker, second int, r request.Result) {
	tr.observe(Check{At: at(second), Result: r, OK: accepted(tr.target, r)})
}

func TestStateTransitionsAndIncidents(t *testing.T) {
	tr := newTracker(Target{FailThreshold: 2, DegradedAfter: 500 * time.Millisecond})
	if tr.state != Unknown {
		t.Fatalf("initial state %s", tr.state)
	}
	feed(tr, 0, ok(100*time.Millisecond))
	if tr.state != Up {
		t.Fatalf("state %s after success", tr.state)
	}
	feed(tr, 10, fail())
	if tr.state != Up || len(tr.incidents) != 0 {
		t.Fatalf("single failure changed state to %s", tr.state)
	}
	feed(tr, 20, ok(100*time.Millisecond))
	feed(tr, 30, fail())
	feed(tr, 40, fail())
	if tr.state != Down || !tr.since.Equal(at(30)) {
		t.Fatalf("state %s since %v, want DOWN since first failure", tr.state, tr.since)
	}
	feed(tr, 50, fail())
	if len(tr.incidents) != 1 || !tr.incidents[0].End.IsZero() || tr.incidents[0].Cause != "network: refused" {
		t.Fatalf("incidents %+v", tr.incidents)
	}
	feed(tr, 60, ok(time.Second))
	if tr.state != Degraded || !tr.incidents[0].End.Equal(at(60)) {
		t.Fatalf("state %s incidents %+v", tr.state, tr.incidents)
	}
	if d := tr.incidents[0].Duration(at(100)); d != 30*time.Second {
		t.Fatalf("incident duration %s", d)
	}
	feed(tr, 70, ok(100*time.Millisecond))
	if tr.state != Up || tr.checks != 8 || tr.failures != 4 {
		t.Fatalf("state %s checks %d failures %d", tr.state, tr.checks, tr.failures)
	}
}

func TestExpectedStatus(t *testing.T) {
	r := request.Result{StatusCode: 503}
	if accepted(Target{}, r) || !accepted(Target{ExpectStatus: []int{503}}, r) || accepted(Target{ExpectStatus: []int{200}}, ok(0)) == false {
		t.Fatal("unexpected status classification")
	}
	if !accepted(Target{}, request.Result{StatusCode: 301}) || accepted(Target{}, request.Result{StatusCode: 404}) {
		t.Fatal("default range must be 200-399")
	}
}

func TestUptimeIsTimeBased(t *testing.T) {
	tr := newTracker(Target{FailThreshold: 1})
	if u := tr.uptime(0, at(10)); u.Observed != 0 {
		t.Fatalf("uptime before checks %+v", u)
	}
	feed(tr, 0, ok(time.Millisecond))
	feed(tr, 100, fail())
	feed(tr, 130, ok(time.Millisecond))
	feed(tr, 200, fail())

	total := tr.uptime(0, at(400))
	// Down 100–130 and 200–400: 230 of 400 seconds.
	if total.Observed != 400*time.Second || math.Abs(total.Percent-42.5) > 1e-9 {
		t.Fatalf("total uptime %+v", total)
	}
	window := tr.uptime(150*time.Second, at(400))
	if window.Observed != 150*time.Second || window.Percent != 0 {
		t.Fatalf("window uptime %+v", window)
	}
	window = tr.uptime(320*time.Second, at(400))
	// Window 80–400: down 30s + 200s of 320s.
	if math.Abs(window.Percent-100*90.0/320) > 1e-9 {
		t.Fatalf("window uptime %+v", window)
	}
}

func TestLatencyDrift(t *testing.T) {
	tr := newTracker(Target{FailThreshold: 1})
	for i := range 50 {
		feed(tr, i, ok(100*time.Millisecond))
	}
	if s := tr.status(at(50)); math.Abs(s.Drift-1) > 1e-9 {
		t.Fatalf("steady drift %f", s.Drift)
	}
	for i := range 10 {
		feed(tr, 50+i, ok(300*time.Millisecond))
	}
	s := tr.status(at(60))
	if s.Drift < 2 || s.Recent < 250*time.Millisecond || s.Baseline > 150*time.Millisecond {
		t.Fatalf("drift %f recent %s baseline %s", s.Drift, s.Recent, s.Baseline)
	}
	if len(s.History) != HistoryLength {
		t.Fatalf("history length %d", len(s.History))
	}
}

func TestNewValidates(t *testing.T) {
	sender := senderFunc(func(context.Context) request.Result { return ok(0) })
	for _, target := range []Target{
		{Name: "a", Interval: time.Second, FailThreshold: 1},
		{Name: "a", Sender: sender, FailThreshold: 1},
		{Name: "a", Sender: sender, Interval: time.Second},
	} {
		if _, err := New([]Target{target}); err == nil {
			t.Errorf("target %+v accepted", target)
		}
	}
	if _, err := New(nil); err == nil {
		t.Error("empty target list accepted")
	}
}

func TestRunSchedulesTargetsUntilCanceled(t *testing.T) {
	var healthy, broken atomic.Int64
	m, err := New([]Target{
		{Name: "healthy", Interval: 10 * time.Millisecond, FailThreshold: 1, Sender: senderFunc(func(context.Context) request.Result {
			healthy.Add(1)
			return ok(time.Millisecond)
		})},
		{Name: "broken", Interval: 10 * time.Millisecond, FailThreshold: 2, Sender: senderFunc(func(context.Context) request.Result {
			broken.Add(1)
			return request.Result{StatusCode: 500}
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var downAt atomic.Int64
	done := make(chan []Status)
	go func() {
		done <- m.Run(ctx, func(u Update) {
			if u.Changed() && u.Status.State == Down {
				downAt.Store(u.Status.Checks)
				cancel()
			}
		})
	}()
	var statuses []Status
	select {
	case statuses = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("monitor did not stop")
	}
	if downAt.Load() != 2 || statuses[1].State != Down || statuses[1].Incidents[0].Cause != "HTTP 500" {
		t.Fatalf("down after %d checks, status %+v", downAt.Load(), statuses[1])
	}
	if statuses[0].Name != "healthy" || statuses[0].State != Up || statuses[0].Total.Percent != 100 {
		t.Fatalf("healthy status %+v", statuses[0])
	}
}

func TestRunDiscardsCanceledChecks(t *testing.T) {
	started := make(chan struct{})
	m, _ := New([]Target{{Name: "slow", Interval: 10 * time.Millisecond, FailThreshold: 1, Sender: senderFunc(func(ctx context.Context) request.Result {
		close(started)
		<-ctx.Done()
		return request.Result{ErrorKind: request.ErrorCanceled, Err: ctx.Err()}
	})}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	statuses := m.Run(ctx, func(Update) { t.Error("canceled check reported") })
	if statuses[0].Checks != 0 || statuses[0].State != Unknown {
		t.Fatalf("status %+v", statuses[0])
	}
}

func TestConfig(t *testing.T) {
	t.Setenv("MONITOR_TOKEN", "secret")
	targets, err := LoadConfig(strings.NewReader(`{"targets":[
		{"name":"api","url":"http://example.test/health","headers":{"Authorization":"Bearer ${MONITOR_TOKEN}"},"interval":"5s","expect_status":[204]},
		{"url":"http://example.test/other","fail_threshold":4}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	defaults := TargetConfig{Interval: Duration(time.Minute), Timeout: Duration(time.Second), FailThreshold: 2, Headers: map[string]string{"User-Agent": "httpload"}}
	first, err := targets[0].WithDefaults(defaults).Target()
	if err != nil {
		t.Fatal(err)
	}
	second, err := targets[1].WithDefaults(defaults).Target()
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "api" || first.Interval != 5*time.Second || first.FailThreshold != 2 || first.ExpectStatus[0] != 204 {
		t.Fatalf("first target %+v", first)
	}
	if second.Name != "http://example.test/other" || second.Interval != time.Minute || second.FailThreshold != 4 {
		t.Fatalf("second target %+v", second)
	}

	missing := TargetConfig{URL: "http://example.test", Headers: map[string]string{"X-Key": "$MONITOR_UNSET_VAR"}}.WithDefaults(defaults)
	if _, err := missing.Target(); err == nil || !strings.Contains(err.Error(), "MONITOR_UNSET_VAR") {
		t.Fatalf("missing variable error %v", err)
	}
	slow := TargetConfig{URL: "http://example.test", Interval: Duration(time.Second), Timeout: Duration(time.Minute)}
	if _, err := slow.Target(); err == nil {
		t.Fatal("timeout above interval accepted")
	}
	for _, bad := range []string{`{"targets":[]}`, `{"targets":[{"url":"x","interval":30}]}`, `{"targets":[{"urll":"x"}]}`} {
		if _, err := LoadConfig(strings.NewReader(bad)); err == nil {
			t.Errorf("config %s accepted", bad)
		}
	}
}
