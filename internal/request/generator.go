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

type Config struct {
	URL     string
	Method  string
	Header  http.Header
	Body    []byte
	Timeout time.Duration
}

type ErrorKind string

const (
	ErrorRequest      ErrorKind = "request"
	ErrorNetwork      ErrorKind = "network"
	ErrorTimeout      ErrorKind = "timeout"
	ErrorCanceled     ErrorKind = "canceled"
	ErrorResponseBody ErrorKind = "response_body"
)

type Result struct {
	StatusCode int
	Duration   time.Duration
	BytesRead  int64
	ErrorKind  ErrorKind
	Err        error
}

type Generator struct {
	url    string
	method string
	header http.Header
	body   []byte
	client *http.Client
}

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
