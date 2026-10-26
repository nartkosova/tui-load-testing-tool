package tui

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nartkosova/tui-load-testing-tool/internal/load"
)

const (
	laneWidth       = 48
	historyLength   = laneWidth
	frameInterval   = 50 * time.Millisecond
	progressPeriod  = 100 * time.Millisecond
	celebrateFrames = 40
)

var (
	spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	sparkLevels   = []rune("▁▂▃▄▅▆▇█")
	confetti      = []rune("✦✧*+·•")
	confettiColor = []lipgloss.Color{"205", "214", "226", "84", "45", "141"}

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Width(9)
	valueStyle = lipgloss.NewStyle().Bold(true)
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("84"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	barStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("45"))
	sparkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
	boxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("63")).Padding(0, 2)
)

// Settings is the load test configuration edited in the form.
type Settings struct {
	URL         string
	Method      string
	Pattern     string
	Concurrency int
	Requests    int64
	Duration    time.Duration
	Timeout     time.Duration
}

// StartFunc validates settings and builds the runner for one test.
type StartFunc func(Settings) (*load.Runner, error)

// Result is the last test run in the session. Ran is false when the user
// quit without starting a test.
type Result struct {
	Settings Settings
	Summary  load.Summary
	Err      error
	Ran      bool
}

type state int

const (
	editing state = iota
	running
	finished
)

type progressMsg load.Summary

type doneMsg struct {
	summary load.Summary
	err     error
}

type frameMsg struct{}

type packet struct {
	pos   int
	speed int
	err   bool
}

type model struct {
	ctx     context.Context
	start   StartFunc
	form    form
	state   state
	info    Settings
	updates chan tea.Msg
	cancel  context.CancelFunc

	summary  load.Summary
	history  []float64
	rate     float64
	peak     float64
	last     load.Summary
	packets  []packet
	frame    int
	stopping bool
	err      error
	party    int
	ran      bool
}

// Run opens the configuration form, runs tests from it and shows the live
// dashboard. Pressing q or Ctrl+C during a test cancels it and keeps the
// partial summary; after a test r returns to the form.
func Run(ctx context.Context, initial Settings, start StartFunc) (Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := model{ctx: ctx, start: start, form: newForm(initial), cancel: func() {}}
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return Result{}, err
	}
	m = final.(model)
	if m.state == running {
		m.cancel()
		m.err = context.Canceled
	}
	return Result{Settings: m.info, Summary: m.summary, Err: m.err, Ran: m.ran}, nil
}

func (m model) Init() tea.Cmd {
	return nextFrame()
}

func nextFrame() tea.Cmd {
	return tea.Tick(frameInterval, func(time.Time) tea.Msg { return frameMsg{} })
}

func waitFor(updates <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-updates }
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.key(msg)
	case progressMsg:
		m.observe(load.Summary(msg))
		return m, waitFor(m.updates)
	case doneMsg:
		m.observe(msg.summary)
		m.state, m.err = finished, msg.err
		m.party = celebrateFrames
		if m.ctx.Err() != nil {
			return m, tea.Quit
		}
	case frameMsg:
		m.frame++
		m.animate()
		return m, nextFrame()
	}
	return m, nil
}

func (m model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch m.state {
	case editing:
		switch key {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			return m.begin()
		}
		m.form.update(msg)
	case running:
		switch key {
		case "q", "ctrl+c", "esc":
			m.stopping = true
			m.cancel()
		}
	case finished:
		switch key {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "r", "enter":
			m.state = editing
		}
	}
	return m, nil
}

// begin builds a runner from the form and starts it in the background.
// Progress snapshots are dropped while the dashboard is busy; the final
// summary is always delivered.
func (m model) begin() (tea.Model, tea.Cmd) {
	settings, err := m.form.settings()
	var runner *load.Runner
	if err == nil {
		runner, err = m.start(settings)
	}
	if err != nil {
		m.form.err = err.Error()
		return m, nil
	}
	m.form.err = ""
	ctx, cancel := context.WithCancel(m.ctx)
	updates := make(chan tea.Msg, 1)
	go func() {
		defer cancel()
		summary, err := runner.RunWithProgress(ctx, progressPeriod, func(s load.Summary) {
			select {
			case updates <- progressMsg(s):
			default:
			}
		})
		updates <- doneMsg{summary, err}
	}()
	m = model{
		ctx: m.ctx, start: m.start, form: m.form, state: running, info: settings,
		updates: updates, cancel: cancel, frame: m.frame, ran: true,
	}
	return m, waitFor(updates)
}

// observe updates the smoothed request rate from consecutive snapshots.
func (m *model) observe(s load.Summary) {
	if elapsed := (s.Duration - m.last.Duration).Seconds(); elapsed > 0 {
		current := float64(s.Requests-m.last.Requests) / elapsed
		m.rate = 0.6*current + 0.4*m.rate
		m.peak = max(m.peak, m.rate)
		m.history = append(m.history, m.rate)
		if len(m.history) > historyLength {
			m.history = m.history[1:]
		}
	}
	m.summary, m.last = s, s
}

// animate moves request packets along the lane. More traffic spawns more
// packets, and packets turn red in proportion to the current error share.
func (m *model) animate() {
	if m.party > 0 {
		m.party--
	}
	moved := m.packets[:0]
	for _, p := range m.packets {
		p.pos += p.speed
		if p.pos < laneWidth {
			moved = append(moved, p)
		}
	}
	m.packets = moved
	if m.state != running {
		return
	}
	traffic := 0.05
	if m.peak > 0 {
		traffic = max(traffic, m.rate/m.peak)
	}
	if rand.Float64() < traffic {
		errShare := 0.0
		if m.summary.Requests > 0 {
			errShare = float64(m.summary.Errors+m.summary.HTTPErrors) / float64(m.summary.Requests)
		}
		m.packets = append(m.packets, packet{speed: 1 + rand.IntN(2), err: rand.Float64() < errShare})
	}
}

func (m model) View() string {
	if m.state == editing {
		return boxStyle.Render(m.form.view(m.frame)) + "\n" + dimStyle.Render(" "+m.help()) + "\n"
	}
	var b strings.Builder
	b.WriteString(m.header() + "\n\n")
	b.WriteString(m.lane() + "\n\n")
	b.WriteString(m.progress() + "\n\n")
	b.WriteString(m.stats() + "\n\n")
	b.WriteString(histogram(m.summary.SuccessLatency))
	return boxStyle.Render(b.String()) + "\n" + dimStyle.Render(" "+m.help()) + "\n"
}

func (m model) header() string {
	icon := spinnerFrames[m.frame%len(spinnerFrames)]
	status := "running"
	switch {
	case m.state == finished && m.err != nil:
		icon, status = errStyle.Render("■"), "stopped"
	case m.state == finished:
		icon, status = okStyle.Render("✔"), "finished"
	case m.stopping:
		status = "stopping"
	}
	return fmt.Sprintf("%s %s %s\n%s", icon, titleStyle.Render("httpload"), dimStyle.Render(status),
		dimStyle.Render(fmt.Sprintf("%s · %d workers · %s", m.info.Pattern, m.info.Concurrency, m.info.URL)))
}

func (m model) lane() string {
	cells := make([]string, laneWidth)
	for i := range cells {
		cells[i] = dimStyle.Render("─")
	}
	if m.party > 0 {
		for i := range cells {
			if rand.IntN(3) == 0 {
				c := lipgloss.NewStyle().Foreground(confettiColor[rand.IntN(len(confettiColor))])
				cells[i] = c.Render(string(confetti[rand.IntN(len(confetti))]))
			}
		}
	}
	for _, p := range m.packets {
		if p.err {
			cells[p.pos] = errStyle.Render("●")
		} else {
			cells[p.pos] = okStyle.Render("●")
		}
	}
	return "you " + strings.Join(cells, "") + " ▤ server"
}

func (m model) progress() string {
	fraction := 0.0
	if m.info.Requests > 0 {
		fraction = float64(m.summary.Requests) / float64(m.info.Requests)
	}
	if m.info.Duration > 0 {
		fraction = max(fraction, m.summary.Duration.Seconds()/m.info.Duration.Seconds())
	}
	if m.state == finished {
		fraction = 1
	}
	fraction = min(fraction, 1)
	filled := int(fraction * laneWidth)
	bar := barStyle.Render(strings.Repeat("█", filled)) + dimStyle.Render(strings.Repeat("░", laneWidth-filled))
	detail := fmt.Sprintf("%s requests", count(m.summary.Requests))
	if m.info.Requests > 0 {
		detail = fmt.Sprintf("%s/%s requests", count(m.summary.Requests), count(m.info.Requests))
	}
	return fmt.Sprintf("%s %3.0f%%  %s  %s", bar, fraction*100, dimStyle.Render(detail),
		dimStyle.Render(m.summary.Duration.Round(100*time.Millisecond).String()))
}

func (m model) stats() string {
	s := m.summary
	rate := m.rate
	if m.state == finished {
		rate, _ = s.RequestRate()
	}
	latency := s.SuccessLatency
	lat := dimStyle.Render("waiting for responses…")
	if latency.Available() {
		lat = fmt.Sprintf("p50 %s  p95 %s  p99 %s  max %s",
			valueStyle.Render(short(latency.P50)), valueStyle.Render(short(latency.P95)),
			valueStyle.Render(short(latency.P99)), valueStyle.Render(short(latency.Max)))
	}
	errors := okStyle.Render("0")
	if s.Errors > 0 {
		errors = errStyle.Render(count(s.Errors))
	}
	httpErrors := okStyle.Render("0")
	if s.HTTPErrors > 0 {
		httpErrors = errStyle.Render(count(s.HTTPErrors))
	}
	rows := []string{
		labelStyle.Render("req/s") + valueStyle.Width(10).Render(count(int64(rate))) + sparkStyle.Render(m.sparkline()),
		labelStyle.Render("results") + fmt.Sprintf("ok %s  errors %s  HTTP errors %s",
			okStyle.Render(count(s.SuccessLatency.Count+s.SuccessLatency.Rejected)), errors, httpErrors),
		labelStyle.Render("latency") + lat,
	}
	return strings.Join(rows, "\n")
}

func (m model) sparkline() string {
	if m.peak == 0 {
		return ""
	}
	var b strings.Builder
	for _, v := range m.history {
		b.WriteRune(sparkLevels[min(int(v/m.peak*float64(len(sparkLevels)-1)+0.5), len(sparkLevels)-1)])
	}
	return b.String()
}

func (m model) help() string {
	switch m.state {
	case editing:
		return "↑/↓ move · ←/→ change option · enter start · esc quit"
	case finished:
		return "r new test · q quit"
	}
	return "q stop test"
}

// count formats n with thousands separators.
func count(n int64) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return s
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func short(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return d.Round(time.Microsecond).String()
	case d < time.Second:
		return d.Round(10 * time.Microsecond).String()
	default:
		return d.Round(time.Millisecond).String()
	}
}
