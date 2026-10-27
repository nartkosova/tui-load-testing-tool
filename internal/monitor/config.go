package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/nartkosova/tui-load-testing-tool/internal/request"
)

const (
	DefaultInterval      = 30 * time.Second
	DefaultTimeout       = 10 * time.Second
	DefaultFailThreshold = 2
)

// Duration reads a JSON string such as "30s".
type Duration time.Duration

func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return errors.New(`duration must be a string such as "30s"`)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

// TargetConfig is one target in a configuration file. Zero values are filled
// from defaults. ${VAR} references in the URL and header values are
// expanded from the environment so credentials stay out of the file.
type TargetConfig struct {
	Name          string            `json:"name"`
	URL           string            `json:"url"`
	Method        string            `json:"method"`
	Headers       map[string]string `json:"headers"`
	Body          string            `json:"body"`
	Interval      Duration          `json:"interval"`
	Timeout       Duration          `json:"timeout"`
	FailThreshold int               `json:"fail_threshold"`
	Degraded      Duration          `json:"degraded"`
	ExpectStatus  []int             `json:"expect_status"`
}

type fileConfig struct {
	Targets []TargetConfig `json:"targets"`
}

func LoadConfig(r io.Reader) ([]TargetConfig, error) {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var cfg fileConfig
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	if len(cfg.Targets) == 0 {
		return nil, errors.New("configuration has no targets")
	}
	return cfg.Targets, nil
}

// WithDefaults fills unset fields from defaults.
func (c TargetConfig) WithDefaults(defaults TargetConfig) TargetConfig {
	if c.Method == "" {
		c.Method = defaults.Method
	}
	if c.Interval == 0 {
		c.Interval = defaults.Interval
	}
	if c.Timeout == 0 {
		c.Timeout = min(defaults.Timeout, c.Interval)
	}
	if c.FailThreshold == 0 {
		c.FailThreshold = defaults.FailThreshold
	}
	if c.Degraded == 0 {
		c.Degraded = defaults.Degraded
	}
	if c.ExpectStatus == nil {
		c.ExpectStatus = defaults.ExpectStatus
	}
	header := make(map[string]string, len(defaults.Headers)+len(c.Headers))
	for k, v := range defaults.Headers {
		header[k] = v
	}
	for k, v := range c.Headers {
		header[k] = v
	}
	c.Headers = header
	return c
}

// Target builds a monitoring target with its own request generator.
func (c TargetConfig) Target() (Target, error) {
	url, err := expand(c.URL)
	if err != nil {
		return Target{}, err
	}
	name := c.Name
	if name == "" {
		name = c.URL
	}
	header := make(http.Header)
	for k, v := range c.Headers {
		value, err := expand(v)
		if err != nil {
			return Target{}, fmt.Errorf("target %q header %s: %w", name, k, err)
		}
		header.Set(k, value)
	}
	if c.Interval > 0 && c.Timeout > c.Interval {
		return Target{}, fmt.Errorf("target %q: timeout must not exceed the interval", name)
	}
	generator, err := request.NewGenerator(request.Config{
		URL: url, Method: c.Method, Header: header, Body: []byte(c.Body), Timeout: time.Duration(c.Timeout),
	})
	if err != nil {
		return Target{}, fmt.Errorf("target %q: %w", name, err)
	}
	return Target{
		Name: name, Sender: generator, Interval: time.Duration(c.Interval), FailThreshold: c.FailThreshold,
		DegradedAfter: time.Duration(c.Degraded), ExpectStatus: c.ExpectStatus,
	}, nil
}

// expand replaces ${VAR} and $VAR with environment values and rejects unset
// variables, so a missing credential fails at startup instead of sending an
// empty header.
func expand(s string) (string, error) {
	var missing []string
	out := os.Expand(s, func(name string) string {
		value, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
		}
		return value
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("environment variable %s is not set", missing[0])
	}
	return out, nil
}
