package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nartkosova/tui-load-testing-tool/internal/load"
	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

type senderFunc func(context.Context) request.Result

func (f senderFunc) Send(ctx context.Context) request.Result { return f(ctx) }

func update(t *testing.T, m model, msg tea.Msg) (model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(model), cmd
}

func keys(t *testing.T, m model, input ...string) model {
	t.Helper()
	for _, k := range input {
		var msg tea.KeyMsg
		switch k {
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		case "ctrl+u":
			msg = tea.KeyMsg{Type: tea.KeyCtrlU}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		m, _ = update(t, m, msg)
	}
	return m
}

func newModel(start StartFunc) model {
	return model{ctx: context.Background(), start: start, form: newForm(Settings{URL: "http://localhost"}), cancel: func() {}}
}

func TestFormEditsSettings(t *testing.T) {
	m := keys(t, newModel(nil),
		"down", "right", // method GET -> POST
		"down", "right", "right", // pattern -> pipeline
		"down", "backspace", "2", "5", // workers 25
		"down", "5", "0", "0", // requests 500
		"down", "1", "0", "s", // duration 10s
		"down", "2", "s", // timeout 2s
	)
	s, err := m.form.settings()
	want := Settings{URL: "http://localhost", Method: "POST", Pattern: "pipeline", Concurrency: 25, Requests: 500, Duration: 10 * time.Second, Timeout: 2 * time.Second}
	if err != nil || s != want {
		t.Fatalf("settings = %+v, %v", s, err)
	}
	if view := m.View(); !strings.Contains(view, "‹ pipeline ›") || !strings.Contains(view, "enter start") {
		t.Errorf("unexpected form view:\n%s", view)
	}
}

func TestFormShowsValidationErrors(t *testing.T) {
	m := keys(t, newModel(func(Settings) (*load.Runner, error) { return nil, errors.New("URL must be absolute") }),
		"down", "down", "down", "x")
	m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != editing || !strings.Contains(m.View(), "workers must be a whole number") {
		t.Fatalf("invalid workers accepted:\n%s", m.View())
	}
	m = keys(t, m, "backspace")
	m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != editing || !strings.Contains(m.View(), "URL must be absolute") {
		t.Fatalf("start error not shown:\n%s", m.View())
	}
}

func TestRunFromFormToResultsAndBack(t *testing.T) {
	start := func(s Settings) (*load.Runner, error) {
		return load.NewRunner(load.Config{Pattern: load.Pattern(s.Pattern), Concurrency: s.Concurrency, Requests: s.Requests},
			senderFunc(func(context.Context) request.Result {
				return request.Result{StatusCode: 200, Duration: time.Millisecond}
			}))
	}
	m := keys(t, newModel(start), "down", "down", "down", "down", "2", "0")
	m, cmd := update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != running || cmd == nil {
		t.Fatalf("test did not start, form error: %q", m.form.err)
	}
	for m.state == running {
		m, cmd = update(t, m, cmd())
	}
	view := m.View()
	for _, want := range []string{"finished", "100%", "20/20 requests", "latency distribution", "r new test"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	m = keys(t, m, "r")
	if m.state != editing || !m.ran {
		t.Fatal("r did not return to the form")
	}
}

func TestQuitDuringRunCancelsTest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := model{state: running, cancel: cancel}
	m, cmd := update(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if ctx.Err() == nil || !m.stopping || cmd != nil {
		t.Fatalf("ctrl+c did not cancel the running test: stopping=%v err=%v", m.stopping, ctx.Err())
	}
	if !strings.Contains(m.View(), "stopping") {
		t.Error("view does not show stopping state")
	}
}

func TestHistogramRendersBuckets(t *testing.T) {
	stats := load.LatencyStats{Count: 12, Buckets: []load.LatencyBucket{
		{From: time.Millisecond, To: 2 * time.Millisecond, Count: 8},
		{From: 2 * time.Millisecond, To: 3 * time.Millisecond, Count: 4},
		{From: 3 * time.Millisecond, To: 4 * time.Millisecond, Count: 0},
	}}
	out := histogram(stats)
	if !strings.Contains(out, "1ms") || !strings.Contains(out, "4ms") || !strings.Contains(out, "peak 8") || !strings.Contains(out, "█") {
		t.Errorf("unexpected histogram:\n%s", out)
	}
	if !strings.Contains(histogram(load.LatencyStats{}), "no successful responses yet") {
		t.Error("empty histogram not marked unavailable")
	}
}

func TestAnimationKeepsPacketsInsideLane(t *testing.T) {
	m := model{state: running, peak: 100, rate: 100, summary: load.Summary{Requests: 10, HTTPErrors: 5}}
	for range 500 {
		m.animate()
		for _, p := range m.packets {
			if p.pos < 0 || p.pos >= laneWidth {
				t.Fatalf("packet outside lane: %d", p.pos)
			}
		}
	}
	if len(m.packets) == 0 {
		t.Fatal("no packets spawned at full traffic")
	}
	_ = m.lane()
}

func TestCount(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if got := count(n); got != want {
			t.Errorf("count(%d) = %q, want %q", n, got, want)
		}
	}
}
