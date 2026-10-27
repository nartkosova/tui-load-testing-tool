package monitor

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

type Sender interface {
	// Send must honor context cancellation.
	Send(context.Context) request.Result
}

type Target struct {
	Name          string
	Sender        Sender
	Interval      time.Duration
	FailThreshold int           // consecutive failed checks before the target is down
	DegradedAfter time.Duration // successful checks slower than this are degraded; 0 disables
	ExpectStatus  []int         // accepted status codes; empty accepts 200–399
}

// Check is the outcome of one scheduled request against a target.
type Check struct {
	Target int
	At     time.Time
	Result request.Result
	OK     bool
}

// Update is reported after every check. Previous is the state before it.
type Update struct {
	Check    Check
	Previous State
	Status   Status
}

func (u Update) Changed() bool {
	return u.Previous != u.Status.State
}

type Monitor struct {
	targets []Target
}

func New(targets []Target) (*Monitor, error) {
	if len(targets) == 0 {
		return nil, errors.New("at least one target is required")
	}
	for _, t := range targets {
		switch {
		case t.Sender == nil:
			return nil, fmt.Errorf("target %q: request sender is required", t.Name)
		case t.Interval <= 0:
			return nil, fmt.Errorf("target %q: interval must be greater than zero", t.Name)
		case t.FailThreshold <= 0:
			return nil, fmt.Errorf("target %q: fail threshold must be greater than zero", t.Name)
		case t.DegradedAfter < 0:
			return nil, fmt.Errorf("target %q: degraded threshold must not be negative", t.Name)
		}
	}
	return &Monitor{targets: slices.Clone(targets)}, nil
}

func (m *Monitor) Targets() []Target {
	return slices.Clone(m.targets)
}

// Run checks every target on its own interval until ctx is canceled and
// returns the final status of each target. Each target has one scheduling
// goroutine; checks are merged into a single channel and all state is owned
// by the calling goroutine, so report needs no locking but must return
// quickly. Checks interrupted by cancellation are discarded.
func (m *Monitor) Run(ctx context.Context, report func(Update)) []Status {
	checks := make(chan Check)
	var schedulers sync.WaitGroup
	for i, t := range m.targets {
		schedulers.Add(1)
		go func() {
			defer schedulers.Done()
			schedule(ctx, i, t, checks)
		}()
	}
	go func() {
		schedulers.Wait()
		close(checks)
	}()

	trackers := make([]*tracker, len(m.targets))
	for i, t := range m.targets {
		trackers[i] = newTracker(t)
	}
	for c := range checks {
		t := trackers[c.Target]
		previous := t.state
		t.observe(c)
		if report != nil {
			report(Update{Check: c, Previous: previous, Status: t.status(c.At)})
		}
	}

	now := time.Now()
	statuses := make([]Status, len(trackers))
	for i, t := range trackers {
		statuses[i] = t.status(now)
	}
	return statuses
}

// schedule starts after a random offset of up to a tenth of the interval so
// targets with equal intervals do not fire together. A check that outlasts
// the interval makes the ticker drop ticks, so checks never overlap.
func schedule(ctx context.Context, index int, t Target, out chan<- Check) {
	offset := time.NewTimer(rand.N(t.Interval/10 + 1))
	select {
	case <-ctx.Done():
		offset.Stop()
		return
	case <-offset.C:
	}
	ticker := time.NewTicker(t.Interval)
	defer ticker.Stop()
	for {
		at := time.Now()
		result := t.Sender.Send(ctx)
		if ctx.Err() != nil {
			return
		}
		select {
		case out <- Check{Target: index, At: at, Result: result, OK: accepted(t, result)}:
		case <-ctx.Done():
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func accepted(t Target, r request.Result) bool {
	if r.Err != nil {
		return false
	}
	if len(t.ExpectStatus) == 0 {
		return r.StatusCode >= 200 && r.StatusCode < 400
	}
	return slices.Contains(t.ExpectStatus, r.StatusCode)
}

// Cause describes why a check failed.
func Cause(r request.Result) string {
	if r.Err != nil {
		return fmt.Sprintf("%s: %v", r.ErrorKind, r.Err)
	}
	return fmt.Sprintf("HTTP %d", r.StatusCode)
}
