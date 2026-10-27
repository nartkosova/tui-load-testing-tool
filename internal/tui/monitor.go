package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nartkosova/tui-load-testing-tool/internal/monitor"
)

const (
	refreshInterval = time.Second
	monitorSpark    = 30
	shownIncidents  = 5
)

var (
	warnStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	selectedStyle = lipgloss.NewStyle().Bold(true)
)

type updateMsg monitor.Update

type monitorDoneMsg struct{}

type refreshMsg time.Time

type monitorModel struct {
	cancel   context.CancelFunc
	updates  chan tea.Msg
	stopped  chan struct{}
	statuses []monitor.Status
	targets  []monitor.Target
	selected int
	detail   bool
	now      time.Time
	stopping bool
}

// RunMonitor shows the live state of every target until q or Ctrl+C and
// returns the final statuses. The monitor keeps running while the UI is
// busy; updates wait for the UI instead of being dropped, so no state
// change is missed.
func RunMonitor(ctx context.Context, m *monitor.Monitor) ([]monitor.Status, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	targets := m.Targets()
	model := monitorModel{
		cancel: cancel, updates: make(chan tea.Msg, len(targets)), stopped: make(chan struct{}),
		statuses: make([]monitor.Status, len(targets)), targets: targets, now: time.Now(),
	}
	for i, t := range targets {
		model.statuses[i].Name = t.Name
	}
	done := make(chan []monitor.Status, 1)
	go func() {
		final := m.Run(ctx, func(u monitor.Update) {
			select {
			case model.updates <- updateMsg(u):
			case <-ctx.Done():
			}
		})
		done <- final
		close(model.stopped)
	}()
	_, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	cancel()
	return <-done, err
}

func (m monitorModel) Init() tea.Cmd {
	return tea.Batch(m.wait(), refresh())
}

// wait delivers the next update, or ends the UI once the monitor stopped.
func (m monitorModel) wait() tea.Cmd {
	return func() tea.Msg {
		select {
		case u := <-m.updates:
			return u
		case <-m.stopped:
			return monitorDoneMsg{}
		}
	}
}

func refresh() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return refreshMsg(t) })
}

func (m monitorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.key(msg)
	case updateMsg:
		m.statuses[msg.Check.Target] = msg.Status
		return m, m.wait()
	case monitorDoneMsg:
		return m, tea.Quit
	case refreshMsg:
		m.now = time.Time(msg)
		return m, refresh()
	}
	return m, nil
}

func (m monitorModel) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.stopping = true
		m.cancel()
	case "esc":
		if m.detail {
			m.detail = false
		} else {
			m.stopping = true
			m.cancel()
		}
	case "up", "k":
		m.selected = max(m.selected-1, 0)
	case "down", "j":
		m.selected = min(m.selected+1, len(m.statuses)-1)
	case "enter":
		m.detail = !m.detail
	}
	return m, nil
}

func (m monitorModel) View() string {
	body := m.list()
	help := "↑/↓ select · enter details · q quit"
	if m.detail {
		body = m.details(m.statuses[m.selected], m.targets[m.selected])
		help = "↑/↓ switch target · esc back · q quit"
	}
	if m.stopping {
		help = "stopping…"
	}
	return boxStyle.Render(m.header()+"\n\n"+body) + "\n" + dimStyle.Render(" "+help) + "\n"
}

func (m monitorModel) header() string {
	counts := map[monitor.State]int{}
	for _, s := range m.statuses {
		counts[s.State]++
	}
	parts := []string{fmt.Sprintf("%d targets", len(m.statuses)), okStyle.Render(fmt.Sprintf("%d up", counts[monitor.Up]))}
	if n := counts[monitor.Degraded]; n > 0 {
		parts = append(parts, warnStyle.Render(fmt.Sprintf("%d degraded", n)))
	}
	if n := counts[monitor.Down]; n > 0 {
		parts = append(parts, errStyle.Render(fmt.Sprintf("%d down", n)))
	}
	return titleStyle.Render("httpload monitor") + "  " + strings.Join(parts, dimStyle.Render(" · "))
}

func (m monitorModel) list() string {
	cell := lipgloss.NewStyle()
	width := 8
	for _, s := range m.statuses {
		width = max(width, min(lipgloss.Width(s.Name), 32))
	}
	rows := []string{dimStyle.Render(fmt.Sprintf("  %-*s  %-9s %8s %8s %8s  %-*s %s",
		width, "TARGET", "STATE", "1h", "24h", "LATENCY", monitorSpark, "RECENT", "CHECKED"))}
	for i, s := range m.statuses {
		marker := "  "
		name := cell.Width(width).MaxWidth(width).Render(s.Name)
		if i == m.selected {
			marker, name = selectedStyle.Render("▸ "), selectedStyle.Width(width).MaxWidth(width).Render(s.Name)
		}
		latency := "–"
		if s.Checks > 0 {
			latency = short(s.Last.Duration)
		}
		rows = append(rows, fmt.Sprintf("%s%s  %s %s %s %8s  %s %s", marker, name,
			stateLabel(s.State, 9), right(uptime(s.Hour), 8), right(uptime(s.Day), 8), latency,
			cell.Width(monitorSpark).Render(checkSpark(s.History, monitorSpark)), dimStyle.Render(m.ago(s.LastCheck))))
	}
	return strings.Join(rows, "\n")
}

func (m monitorModel) details(s monitor.Status, t monitor.Target) string {
	since := ""
	if !s.Since.IsZero() {
		since = dimStyle.Render(" for " + m.since(s.Since))
	}
	rows := []string{
		selectedStyle.Render(s.Name) + "  " + stateLabel(s.State, 0) + since,
		dimStyle.Render(fmt.Sprintf("every %s · down after %d failed checks", t.Interval, t.FailThreshold)),
		"",
		labelStyle.Render("uptime") + fmt.Sprintf("1h %s  24h %s  all %s", uptime(s.Hour), uptime(s.Day), uptime(s.Total)),
		labelStyle.Render("checks") + fmt.Sprintf("%s  failed %s", count(s.Checks), count(s.Failures)),
		labelStyle.Render("latency") + m.drift(s),
		labelStyle.Render("last") + m.last(s),
		labelStyle.Render("recent") + checkSpark(s.History, monitor.HistoryLength),
		"",
		dimStyle.Render("incidents"),
	}
	if len(s.Incidents) == 0 {
		rows = append(rows, dimStyle.Render("  none"))
	}
	for i := len(s.Incidents) - 1; i >= max(len(s.Incidents)-shownIncidents, 0); i-- {
		inc := s.Incidents[i]
		end := errStyle.Render("ongoing")
		if !inc.End.IsZero() {
			end = inc.End.Format("15:04:05")
		}
		rows = append(rows, fmt.Sprintf("  %s → %s  %s  %s", inc.Start.Format("2006-01-02 15:04:05"), end,
			valueStyle.Render(inc.Duration(m.now).Round(time.Second).String()), dimStyle.Render(truncate(inc.Cause, 50))))
	}
	return strings.Join(rows, "\n")
}

func (m monitorModel) drift(s monitor.Status) string {
	if s.Drift == 0 {
		return dimStyle.Render("no successful checks")
	}
	ratio := fmt.Sprintf("%.2f×", s.Drift)
	switch {
	case s.Drift >= 2:
		ratio = errStyle.Render(ratio)
	case s.Drift >= 1.5:
		ratio = warnStyle.Render(ratio)
	}
	return fmt.Sprintf("recent %s  baseline %s  drift %s", short(s.Recent), short(s.Baseline), ratio)
}

func (m monitorModel) last(s monitor.Status) string {
	if s.Checks == 0 {
		return dimStyle.Render("waiting for first check")
	}
	result := okStyle.Render(fmt.Sprintf("HTTP %d", s.Last.StatusCode))
	if !s.History[len(s.History)-1].OK {
		result = errStyle.Render(truncate(monitor.Cause(s.Last), 60))
	}
	return fmt.Sprintf("%s in %s %s", result, short(s.Last.Duration), dimStyle.Render(m.ago(s.LastCheck)))
}

func (m monitorModel) ago(t time.Time) string {
	if t.IsZero() {
		return "–"
	}
	return m.since(t) + " ago"
}

func (m monitorModel) since(t time.Time) string {
	return max(m.now.Sub(t), 0).Truncate(time.Second).String()
}

// right pads a styled string to width; fmt widths would count escape codes.
func right(s string, width int) string {
	return lipgloss.NewStyle().Width(width).Align(lipgloss.Right).Render(s)
}

func truncate(s string, width int) string {
	if r := []rune(s); len(r) > width {
		return string(r[:width-1]) + "…"
	}
	return s
}

func stateLabel(s monitor.State, width int) string {
	style := dimStyle
	icon := "○"
	switch s {
	case monitor.Up:
		style, icon = okStyle, "●"
	case monitor.Degraded:
		style, icon = warnStyle, "●"
	case monitor.Down:
		style, icon = errStyle, "●"
	}
	label := icon + " " + s.String()
	if width > 0 {
		label = fmt.Sprintf("%-*s", width, label)
	}
	return style.Render(label)
}

func uptime(u monitor.Uptime) string {
	if u.Observed == 0 {
		return "–"
	}
	text := fmt.Sprintf("%.2f%%", u.Percent)
	switch {
	case u.Percent < 99:
		return errStyle.Render(text)
	case u.Percent < 100:
		return warnStyle.Render(text)
	}
	return text
}

// checkSpark draws the latency of the last width checks scaled to the
// slowest one, with failed checks as red crosses.
func checkSpark(history []monitor.Sample, width int) string {
	history = history[max(len(history)-width, 0):]
	var peak time.Duration
	for _, s := range history {
		if s.OK {
			peak = max(peak, s.Latency)
		}
	}
	var b strings.Builder
	for _, s := range history {
		if !s.OK {
			b.WriteString(errStyle.Render("✕"))
			continue
		}
		level := 0
		if peak > 0 {
			level = min(int(float64(s.Latency)/float64(peak)*float64(len(sparkLevels)-1)+0.5), len(sparkLevels)-1)
		}
		b.WriteString(sparkStyle.Render(string(sparkLevels[level])))
	}
	return b.String()
}
