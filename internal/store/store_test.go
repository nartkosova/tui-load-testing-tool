package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nartkosova/tui-load-testing-tool/internal/load"
	"github.com/nartkosova/tui-load-testing-tool/internal/monitor"
	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

func TestStoreRecordsChecksIncidentsAndRuns(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	start := time.Unix(1000, 0)
	fail := request.Result{Err: errors.New("refused"), ErrorKind: request.ErrorKind("network")}
	updates := []monitor.Update{
		{Check: monitor.Check{At: start, OK: true, Result: request.Result{StatusCode: 200}}, Previous: monitor.Unknown,
			Status: monitor.Status{Name: "api", State: monitor.Up}},
		{Check: monitor.Check{At: start.Add(time.Second), Result: fail}, Previous: monitor.Up,
			Status: monitor.Status{Name: "api", State: monitor.Down, Since: start.Add(time.Second)}},
		{Check: monitor.Check{At: start.Add(3 * time.Second), OK: true, Result: request.Result{StatusCode: 200}},
			Previous: monitor.Down, Status: monitor.Status{Name: "api", State: monitor.Up}},
	}
	for _, u := range updates {
		if err := s.RecordCheck(u); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordLoadRun(start, "http://x", "pipeline", 4, load.Summary{Requests: 10}); err != nil {
		t.Fatal(err)
	}

	var checks, failed, runs int
	var incStart, incEnd int64
	var cause string
	s.db.QueryRow(`SELECT count(*), sum(NOT ok) FROM checks`).Scan(&checks, &failed)
	s.db.QueryRow(`SELECT start, end, cause FROM incidents`).Scan(&incStart, &incEnd, &cause)
	s.db.QueryRow(`SELECT count(*) FROM load_runs WHERE requests = 10`).Scan(&runs)
	if checks != 3 || failed != 1 || runs != 1 {
		t.Errorf("checks=%d failed=%d runs=%d", checks, failed, runs)
	}
	if incStart != start.Add(time.Second).UnixNano() || incEnd != start.Add(3*time.Second).UnixNano() || cause != "network: refused" {
		t.Errorf("incident %d–%d %q", incStart, incEnd, cause)
	}
}
