package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSingleRequestOutputAndConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "payload" || r.Method != http.MethodPost || len(r.Header.Values("X-Test")) != 2 {
			t.Errorf("unexpected request: method=%s headers=%v body=%s err=%v", r.Method, r.Header, body, err)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "busy")
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-url", server.URL, "-method", "POST", "-body", "payload", "-header", "X-Test: one", "-header", "X-Test: two"}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "status: 503\n") || !strings.Contains(stdout.String(), "response bytes: 4\n") || strings.Contains(stdout.String(), "pattern:") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestLoadPatternsCLI(t *testing.T) {
	for _, pattern := range []string{"worker-pool", "fan-out-fan-in", "pipeline"} {
		t.Run(pattern, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, "bad")
			}))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"-url", server.URL, "-pattern", pattern, "-concurrency", "3", "-requests", "9", "-duration", "1h"}, &stdout, &stderr)
			if code != 0 || stderr.Len() != 0 || calls.Load() != 9 {
				t.Fatalf("code=%d calls=%d stdout=%q stderr=%q", code, calls.Load(), stdout.String(), stderr.String())
			}
			for _, want := range []string{"pattern: " + pattern + "\n", "concurrency: 3\n", "requests: 9\n", "responses: 9\n", "errors: 0\n", "HTTP errors: 9\n", "response bytes: 27\n"} {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("output %q missing %q", stdout.String(), want)
				}
			}
		})
	}
}

func TestDurationCLIContinuesUntilCanceled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 3 {
			cancel()
		}
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"-url", server.URL, "-duration", "1h"}, &stdout, &stderr)
	if code != 1 || !errors.Is(ctx.Err(), context.Canceled) || calls.Load() != 3 || !strings.Contains(stderr.String(), "load test stopped: context canceled") || !strings.Contains(stdout.String(), "pattern: worker-pool") {
		t.Fatalf("code=%d calls=%d stdout=%q stderr=%q", code, calls.Load(), stdout.String(), stderr.String())
	}
}

func TestInvalidCLIConfigurationDoesNotSendRequests(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	tests := [][]string{
		{"-pattern", "unknown"},
		{"-concurrency", "0"},
		{"-concurrency", "-1"},
		{"-requests", "0"},
		{"-requests", "-1"},
		{"-duration", "-1s"},
		{"-duration", "invalid"},
		{"-timeout", "0"},
		{"-header", "invalid"},
		{"-unknown"},
		{"unexpected"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), append([]string{"-url", server.URL}, args...), &stdout, &stderr)
			if code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid configuration sent %d requests", calls.Load())
	}
}

func TestCLIHelpAndMissingURL(t *testing.T) {
	for _, test := range []struct {
		args []string
		code int
	}{{[]string{"-help"}, 0}, {nil, 2}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), test.args, &stdout, &stderr); code != test.code {
			t.Errorf("args=%v code=%d want=%d", test.args, code, test.code)
		}
	}
}
