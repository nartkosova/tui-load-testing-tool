package monitor

import (
	"slices"
	"time"

	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

type State int

const (
	Unknown State = iota
	Up
	Degraded
	Down
)

func (s State) String() string {
	switch s {
	case Up:
		return "UP"
	case Degraded:
		return "DEGRADED"
	case Down:
		return "DOWN"
	}
	return "UNKNOWN"
}

const (
	// HistoryLength is the number of recent checks kept per target.
	HistoryLength = 60
	// maxIncidents bounds memory until incidents are persisted; uptime
	// windows lose accuracy only if more incidents happen inside them.
	maxIncidents = 100
	// Smoothing factors for the recent and baseline latency averages.
	recentAlpha   = 0.3
	baselineAlpha = 0.02
)

// Incident is a period in which a target was down. It starts at the first
// failed check of the streak that crossed the fail threshold and ends at the
// next successful check. End is zero while the incident is open.
type Incident struct {
	Start time.Time
	End   time.Time
	Cause string
}

func (i Incident) Duration(now time.Time) time.Duration {
	if i.End.IsZero() {
		return now.Sub(i.Start)
	}
	return i.End.Sub(i.Start)
}

type Sample struct {
	Latency time.Duration
	OK      bool
}

// Uptime is the share of observed time in which the target was not down.
// Observation starts at the first check, so a window longer than the
// monitoring period covers only the monitored part.
type Uptime struct {
	Percent  float64
	Observed time.Duration
}

type Status struct {
	Name      string
	State     State
	Since     time.Time
	Checks    int64
	Failures  int64
	LastCheck time.Time
	Last      request.Result

	Hour, Day, Total Uptime

	// Recent and Baseline are exponential moving averages of successful
	// check latency; Drift is Recent/Baseline, 0 until there is a sample.
	Recent, Baseline time.Duration
	Drift            float64

	History   []Sample
	Incidents []Incident
}

type tracker struct {
	target    Target
	state     State
	since     time.Time
	first     time.Time
	checks    int64
	failures  int64
	streak    int
	streakAt  time.Time
	last      Check
	recent    float64
	baseline  float64
	history   []Sample
	incidents []Incident
}

func newTracker(t Target) *tracker {
	return &tracker{target: t}
}

func (t *tracker) observe(c Check) {
	if t.checks == 0 {
		t.first = c.At
	}
	t.checks++
	t.last = c
	t.history = append(t.history, Sample{Latency: c.Result.Duration, OK: c.OK})
	if len(t.history) > HistoryLength {
		t.history = t.history[1:]
	}

	if !c.OK {
		t.failures++
		if t.streak == 0 {
			t.streakAt = c.At
		}
		t.streak++
		if t.streak >= t.target.FailThreshold && t.state != Down {
			t.state, t.since = Down, t.streakAt
			t.incidents = append(t.incidents, Incident{Start: t.streakAt, Cause: Cause(c.Result)})
			if len(t.incidents) > maxIncidents {
				t.incidents = t.incidents[1:]
			}
		}
		return
	}

	t.streak = 0
	if t.state == Down {
		t.incidents[len(t.incidents)-1].End = c.At
	}
	latency := float64(c.Result.Duration)
	if t.baseline == 0 {
		t.recent, t.baseline = latency, latency
	} else {
		t.recent += recentAlpha * (latency - t.recent)
		t.baseline += baselineAlpha * (latency - t.baseline)
	}
	next := Up
	if t.target.DegradedAfter > 0 && c.Result.Duration > t.target.DegradedAfter {
		next = Degraded
	}
	if next != t.state {
		t.state, t.since = next, c.At
	}
}

func (t *tracker) uptime(window time.Duration, now time.Time) Uptime {
	if t.checks == 0 || !now.After(t.first) {
		return Uptime{}
	}
	start := t.first
	if window > 0 && now.Add(-window).After(start) {
		start = now.Add(-window)
	}
	observed := now.Sub(start)
	var down time.Duration
	for _, i := range t.incidents {
		end := i.End
		if end.IsZero() || end.After(now) {
			end = now
		}
		from := i.Start
		if from.Before(start) {
			from = start
		}
		if end.After(from) {
			down += end.Sub(from)
		}
	}
	return Uptime{Percent: 100 * float64(observed-down) / float64(observed), Observed: observed}
}

func (t *tracker) status(now time.Time) Status {
	s := Status{
		Name:      t.target.Name,
		State:     t.state,
		Since:     t.since,
		Checks:    t.checks,
		Failures:  t.failures,
		LastCheck: t.last.At,
		Last:      t.last.Result,
		Hour:      t.uptime(time.Hour, now),
		Day:       t.uptime(24*time.Hour, now),
		Total:     t.uptime(0, now),
		Recent:    time.Duration(t.recent),
		Baseline:  time.Duration(t.baseline),
		History:   slices.Clone(t.history),
		Incidents: slices.Clone(t.incidents),
	}
	if t.baseline > 0 {
		s.Drift = t.recent / t.baseline
	}
	return s
}
