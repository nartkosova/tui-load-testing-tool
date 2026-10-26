package tui

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	methods  = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead}
	patterns = []string{"worker-pool", "fan-out-fan-in", "pipeline"}

	focusStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)
	choiceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("45")).Bold(true)
)

const (
	fieldURL = iota
	fieldMethod
	fieldPattern
	fieldConcurrency
	fieldRequests
	fieldDuration
	fieldTimeout
)

// field is either free text or, when choices is set, a fixed option list.
type field struct {
	label   string
	hint    string
	value   string
	choices []string
}

type form struct {
	fields []field
	focus  int
	err    string
}

func newForm(s Settings) form {
	durationText := func(d time.Duration) string {
		if d == 0 {
			return ""
		}
		return d.String()
	}
	requests := ""
	if s.Requests > 0 {
		requests = strconv.FormatInt(s.Requests, 10)
	}
	return form{fields: []field{
		fieldURL:         {label: "URL", hint: "http(s)://host/path", value: s.URL},
		fieldMethod:      {label: "Method", value: or(s.Method, http.MethodGet), choices: methods},
		fieldPattern:     {label: "Pattern", value: or(s.Pattern, patterns[0]), choices: patterns},
		fieldConcurrency: {label: "Workers", hint: "simultaneous requests", value: strconv.Itoa(max(s.Concurrency, 1))},
		fieldRequests:    {label: "Requests", hint: "empty = until duration ends", value: requests},
		fieldDuration:    {label: "Duration", hint: "e.g. 30s, 2m; empty = no limit", value: durationText(s.Duration)},
		fieldTimeout:     {label: "Timeout", hint: "per request", value: durationText(s.Timeout)},
	}}
}

func or(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (f *form) update(msg tea.KeyMsg) {
	current := &f.fields[f.focus]
	switch msg.String() {
	case "up", "shift+tab":
		f.focus = (f.focus + len(f.fields) - 1) % len(f.fields)
	case "down", "tab":
		f.focus = (f.focus + 1) % len(f.fields)
	case "left", "right", " ":
		if current.choices != nil {
			step := 1
			if msg.String() == "left" {
				step = len(current.choices) - 1
			}
			i := indexOf(current.choices, current.value)
			current.value = current.choices[(i+step)%len(current.choices)]
		} else if msg.Type == tea.KeySpace {
			current.value += " "
		}
	case "backspace":
		if current.choices == nil && current.value != "" {
			runes := []rune(current.value)
			current.value = string(runes[:len(runes)-1])
		}
	case "ctrl+u":
		if current.choices == nil {
			current.value = ""
		}
	default:
		if msg.Type == tea.KeyRunes && current.choices == nil {
			current.value += string(msg.Runes)
		}
	}
}

func indexOf(values []string, value string) int {
	for i, v := range values {
		if v == value {
			return i
		}
	}
	return 0
}

// settings parses the form; the runner factory performs the remaining checks.
func (f form) settings() (Settings, error) {
	value := func(i int) string { return strings.TrimSpace(f.fields[i].value) }
	s := Settings{URL: value(fieldURL), Method: value(fieldMethod), Pattern: value(fieldPattern)}
	var err error
	if s.Concurrency, err = strconv.Atoi(value(fieldConcurrency)); err != nil {
		return s, errors.New("workers must be a whole number")
	}
	if v := value(fieldRequests); v != "" {
		if s.Requests, err = strconv.ParseInt(v, 10, 64); err != nil {
			return s, errors.New("requests must be a whole number")
		}
	}
	if v := value(fieldDuration); v != "" {
		if s.Duration, err = time.ParseDuration(v); err != nil {
			return s, errors.New("duration must look like 30s or 2m")
		}
	}
	s.Timeout = 10 * time.Second
	if v := value(fieldTimeout); v != "" {
		if s.Timeout, err = time.ParseDuration(v); err != nil {
			return s, errors.New("timeout must look like 5s or 500ms")
		}
	}
	return s, nil
}

func (f form) view(frame int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("httpload") + dimStyle.Render(" · configure load test") + "\n\n")
	for i, fl := range f.fields {
		label := labelStyle.Render(fl.label)
		cursor := "  "
		if i == f.focus {
			cursor = focusStyle.Render("▸ ")
			label = focusStyle.Width(9).Render(fl.label)
		}
		var value string
		switch {
		case fl.choices != nil:
			value = "‹ " + choiceStyle.Render(fl.value) + " ›"
		case fl.value == "" && i != f.focus:
			value = dimStyle.Render(fl.hint)
		default:
			value = fl.value
			if i == f.focus {
				if frame/10%2 == 0 {
					value += focusStyle.Render("▌")
				} else {
					value += " "
				}
				if fl.value == "" {
					value += dimStyle.Render(fl.hint)
				}
			}
		}
		fmt.Fprintf(&b, "%s%s%s\n", cursor, label, value)
	}
	b.WriteString("\n")
	if f.err != "" {
		b.WriteString(errStyle.Render("✗ " + f.err))
	} else {
		b.WriteString(dimStyle.Render("press enter to start"))
	}
	return b.String()
}
