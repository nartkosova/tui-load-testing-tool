// Package request executes one configured HTTP request at a time.
// Scheduling and aggregation belong to later layers of the application.
package request

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Config describes the HTTP request to send. A Generator takes its own copy
// of Header and Body, so callers may reuse or change their Config afterward.
type Config struct {
	URL     string
	Method  string
	Header  http.Header
	Body    []byte
	Timeout time.Duration
}

// ErrorKind identifies which part of a request failed. An HTTP error status
// is still a completed response and leaves ErrorKind empty.
type ErrorKind string

const (
	ErrorRequest      ErrorKind = "request"
	ErrorNetwork      ErrorKind = "network"
	ErrorTimeout      ErrorKind = "timeout"
	ErrorCanceled     ErrorKind = "canceled"
	ErrorResponseBody ErrorKind = "response_body"
)

// Result describes one attempt. Duration includes reading the entire response
// body, because that is when the request has actually finished.
type Result struct {
	StatusCode int
	Duration   time.Duration
	BytesRead  int64
	ErrorKind  ErrorKind
	Err        error
}

// Generator owns a reusable HTTP client and an immutable request definition.
// Send is safe to call from multiple goroutines when later phases need it.
type Generator struct {
	url    string
	method string
	header http.Header
	body   []byte
	client *http.Client
}

// NewGenerator validates the request before any network activity occurs.
func NewGenerator(cfg Config) (*Generator, error) {
	if cfg.Timeout <= 0 {
		return nil, errors.New("timeout must be greater than zero")
	}

	parsed, err := url.Parse(cfg.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("URL must be an absolute HTTP or HTTPS URL")
	}

	method := cfg.Method
	if method == "" {
		method = http.MethodGet
	}
	if _, err := http.NewRequest(method, cfg.URL, nil); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	return &Generator{
		url:    cfg.URL,
		method: method,
		header: cfg.Header.Clone(),
		body:   bytes.Clone(cfg.Body),
		client: &http.Client{Timeout: cfg.Timeout},
	}, nil
}

// Send makes one request. Each call gets a fresh body reader, so the same
// Generator can be used for repeated requests.
func (g *Generator) Send(ctx context.Context) Result {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, g.method, g.url, bytes.NewReader(g.body))
	if err != nil {
		return Result{Duration: time.Since(start), ErrorKind: ErrorRequest, Err: err}
	}
	req.Header = g.header.Clone()

	resp, err := g.client.Do(req)
	if err != nil {
		return Result{Duration: time.Since(start), ErrorKind: classifyError(err), Err: err}
	}
	defer resp.Body.Close()

	bytesRead, err := io.Copy(io.Discard, resp.Body)
	result := Result{
		StatusCode: resp.StatusCode,
		Duration:   time.Since(start),
		BytesRead:  bytesRead,
	}
	if err != nil {
		result.ErrorKind = classifyBodyError(err)
		result.Err = err
	}
	return result
}

func classifyError(err error) ErrorKind {
	if errors.Is(err, context.Canceled) {
		return ErrorCanceled
	}
	if isTimeout(err) {
		return ErrorTimeout
	}
	return ErrorNetwork
}

func classifyBodyError(err error) ErrorKind {
	if errors.Is(err, context.Canceled) {
		return ErrorCanceled
	}
	if isTimeout(err) {
		return ErrorTimeout
	}
	return ErrorResponseBody
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
