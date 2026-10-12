package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

type headersFlag http.Header

func (h *headersFlag) String() string {
	return ""
}

func (h *headersFlag) Set(value string) error {
	name, content, ok := strings.Cut(value, ":")
	if !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("header must be in Name: value format")
	}
	http.Header(*h).Add(strings.TrimSpace(name), strings.TrimSpace(content))
	return nil
}

func main() {
	var target, method, body string
	var timeout time.Duration
	var headers = make(headersFlag)

	flag.StringVar(&target, "url", "", "absolute HTTP or HTTPS target URL")
	flag.StringVar(&method, "method", http.MethodGet, "HTTP method")
	flag.StringVar(&body, "body", "", "request body")
	flag.DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	flag.Var(&headers, "header", "request header in 'Name: value' format (repeatable)")
	flag.Parse()

	if target == "" {
		fmt.Fprintln(os.Stderr, "-url is required")
		flag.Usage()
		os.Exit(2)
	}

	generator, err := request.NewGenerator(request.Config{
		URL:     target,
		Method:  method,
		Header:  http.Header(headers),
		Body:    []byte(body),
		Timeout: timeout,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	result := generator.Send(context.Background())
	if result.Err != nil {
		fmt.Fprintf(os.Stderr, "error: %s: %v\n", result.ErrorKind, result.Err)
		os.Exit(1)
	}
	fmt.Printf("status: %d\nduration: %s\nresponse bytes: %d\n", result.StatusCode, result.Duration, result.BytesRead)
}
