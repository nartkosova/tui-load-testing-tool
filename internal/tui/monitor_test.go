package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nartkosova/tui-load-testing-tool/internal/monitor"
	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

func TestMonitorModel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	m := monitorModel{
		cancel: cancel, updates: make(chan tea.Msg), stopped: make(chan struct{}), now: now,
		targets:  []monitor.Target{{Name: "api", Interval: time.Second, FailThreshold: 2}, {Name: "web", Interval: time.Second, FailThreshold: 2}},
		statuses: []monitor.Status{{Name: "api"}, {Name: "web"}},
	}
	view := m.View()
	if !strings.Contains(view, "api") || !strings.Contains(view, "UNKNOWN") {
		t.Fatalf("initial view %q", view)
	}

	down := monitor.Status{
		Name: "web", State: monitor.Down, Since: now.Add(-time.Minute), Checks: 3, Failures: 2, LastCheck: now,
		Last:      request.Result{StatusCode: 503, Duration: 5 * time.Millisecond},
		Total:     monitor.Uptime{Percent: 50, Observed: 2 * time.Minute},
		History:   []monitor.Sample{{Latency: time.Millisecond, OK: true}, {OK: false}, {OK: false}},
		Incidents: []monitor.Incident{{Start: now.Add(-time.Minute), Cause: "HTTP 503"}},
	}
	model, _ := m.Update(updateMsg(monitor.Update{Check: monitor.Check{Target: 1}, Status: down}))
	m = model.(monitorModel)
	if view := m.View(); !strings.Contains(view, "1 down") || !strings.Contains(view, "✕") {
		t.Fatalf("view after update %q", view)
	}

	for _, key := range []tea.KeyType{tea.KeyDown, tea.KeyEnter} {
		model, _ = m.Update(tea.KeyMsg{Type: key})
		m = model.(monitorModel)
	}
	view = m.View()
	for _, want := range []string{"for 1m0s", "50.00%", "ongoing", "HTTP 503", "down after 2 failed checks"} {
		if !strings.Contains(view, want) {
			t.Errorf("detail view %q missing %q", view, want)
		}
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m = model.(monitorModel); m.detail || ctx.Err() != nil {
		t.Fatal("esc in details must return to the list")
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !model.(monitorModel).stopping || ctx.Err() == nil {
		t.Fatal("q must stop the monitor")
	}
	if _, cmd := m.Update(monitorDoneMsg{}); cmd == nil {
		t.Fatal("monitor end must quit")
	}
}
