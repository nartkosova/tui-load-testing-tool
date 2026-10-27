package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMonitorDetectsOutageAndRecovery(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := calls.Add(1); n >= 3 && n <= 5 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for calls.Load() < 8 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"monitor", "-url", server.URL, "-interval", "10ms", "-fail-threshold", "2"}, &stdout, &stderr)
	out := stdout.String()
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr.String())
	}
	for _, want := range []string{"UNKNOWN → UP  HTTP 200", "UP → DOWN since", "HTTP 503", "DOWN → UP after", "incidents: 1\n", "failed checks: 3\n", "uptime: "} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestMonitorConfigFile(t *testing.T) {
	var auth atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
	}))
	defer server.Close()
	t.Setenv("HTTPLOAD_TEST_TOKEN", "abc")
	path := filepath.Join(t.TempDir(), "targets.json")
	config := `{"targets":[{"name":"api","url":"` + server.URL + `","headers":{"Authorization":"Bearer ${HTTPLOAD_TEST_TOKEN}"},"interval":"10ms"}]}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"monitor", "-config", path, "-v"}, &stdout, &stderr)
	if code != 0 || auth.Load() != "Bearer abc" || !strings.Contains(stdout.String(), "api  UNKNOWN → UP  HTTP 200") {
		t.Fatalf("code=%d auth=%v stdout=%q stderr=%q", code, auth.Load(), stdout.String(), stderr.String())
	}
}

func TestMonitorArgumentErrors(t *testing.T) {
	for _, args := range [][]string{
		{"monitor"},
		{"monitor", "-url", "ftp://example.test"},
		{"monitor", "-url", "http://example.test", "-fail-threshold", "0"},
		{"monitor", "-config", filepath.Join(t.TempDir(), "missing.json")},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != 2 || stderr.Len() == 0 {
			t.Errorf("%v: code=%d stderr=%q", args, code, stderr.String())
		}
	}
}
