package request

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGeneratorSendsConfiguredRequestRepeatedly(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.Header.Get("X-Test") != "original" {
			t.Errorf("unexpected request: method=%q header=%q", r.Method, r.Header.Get("X-Test"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "payload" {
			t.Errorf("unexpected body: %q, %v", body, err)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "busy")
	}))
	defer server.Close()

	cfg := Config{
		URL: server.URL, Method: http.MethodPost,
		Header: http.Header{"X-Test": {"original"}},
		Body:   []byte("payload"), Timeout: time.Second,
	}
	generator, err := NewGenerator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Header.Set("X-Test", "changed")
	cfg.Body[0] = 'X'

	for range 2 {
		result := generator.Send(context.Background())
		if result.Err != nil || result.ErrorKind != "" || result.StatusCode != http.StatusServiceUnavailable || result.BytesRead != 4 {
			t.Fatalf("unexpected result: %+v", result)
		}
		if result.Duration <= 0 {
			t.Fatalf("expected a positive duration: %+v", result)
		}
	}
	if requests != 2 {
		t.Fatalf("got %d requests, want 2", requests)
	}
}

func TestGeneratorTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	generator, err := NewGenerator(Config{URL: server.URL, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	result := generator.Send(context.Background())
	if result.Err == nil || result.ErrorKind != ErrorTimeout {
		t.Fatalf("expected timeout, got %+v", result)
	}
}

func TestNewGeneratorRejectsInvalidConfiguration(t *testing.T) {
	tests := []Config{
		{URL: "ftp://example.com", Timeout: time.Second},
		{URL: "https:///missing-host", Timeout: time.Second},
		{URL: "https://example.com", Method: "bad method", Timeout: time.Second},
		{URL: "https://example.com", Timeout: 0},
	}
	for _, cfg := range tests {
		if _, err := NewGenerator(cfg); err == nil {
			t.Errorf("accepted invalid configuration: %+v", cfg)
		}
	}
}

func TestGeneratorCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	generator, err := NewGenerator(Config{URL: server.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := generator.Send(ctx)
	if result.Err == nil || result.ErrorKind != ErrorCanceled {
		t.Fatalf("expected canceled request, got %+v", result)
	}
}

func TestGeneratorReadsResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, strings.NewReader(strings.Repeat("x", 1024)))
	}))
	defer server.Close()

	generator, err := NewGenerator(Config{URL: server.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result := generator.Send(context.Background())
	if result.Err != nil || result.BytesRead != 1024 {
		t.Fatalf("unexpected result: %+v", result)
	}
}
