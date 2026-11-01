package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

func monoNow() int64 {
	var ts syscall.Timespec
	syscall.Syscall(syscall.SYS_CLOCK_GETTIME, 1, uintptr(unsafe.Pointer(&ts)), 0)
	return ts.Nano()
}

type phase struct {
	Name     string  `json:"name"`
	At       float64 `json:"at_s"`
	Status   int     `json:"status"`
	DelayMS  int     `json:"delay_ms"`
	FailNext int64   `json:"fail_next"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8098", "listen address")
	schedulePath := flag.String("schedule", "", "JSON phase schedule")
	logPath := flag.String("log", "", "JSON lines event log")
	flag.Parse()

	data, err := os.ReadFile(*schedulePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var phases []phase
	if err := json.Unmarshal(data, &phases); err != nil || len(phases) == 0 {
		fmt.Fprintln(os.Stderr, "invalid schedule", err)
		os.Exit(2)
	}
	logFile, err := os.Create(*logPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer logFile.Close()

	var mu sync.Mutex
	encoder := json.NewEncoder(logFile)
	write := func(event map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		encoder.Encode(event)
	}

	var current atomic.Pointer[phase]
	var failLeft atomic.Int64
	var sequence atomic.Int64
	activate := func(p *phase, start time.Time) {
		failLeft.Store(p.FailNext)
		current.Store(p)
		now := time.Now()
		write(map[string]any{"event": "phase", "name": p.Name, "t_ns": now.UnixNano(), "mono_ns": monoNow(),
			"rel_s": now.Sub(start).Seconds(), "status": p.Status, "delay_ms": p.DelayMS, "fail_next": p.FailNext})
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	start := time.Now()
	write(map[string]any{"event": "start", "t_ns": start.UnixNano(), "mono_ns": monoNow(), "addr": *addr})
	activate(&phases[0], start)
	go func() {
		for i := 1; i < len(phases); i++ {
			time.Sleep(time.Until(start.Add(time.Duration(phases[i].At * float64(time.Second)))))
			activate(&phases[i], start)
		}
		write(map[string]any{"event": "schedule_end", "t_ns": time.Now().UnixNano(), "mono_ns": monoNow()})
	}()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrive := time.Now()
		arriveMono := monoNow()
		id := sequence.Add(1)
		p := current.Load()
		write(map[string]any{"event": "arrive", "id": id, "phase": p.Name, "t_ns": arrive.UnixNano(), "mono_ns": arriveMono})
		status := p.Status
		if status == 0 {
			status = http.StatusOK
		}
		if p.FailNext > 0 && failLeft.Add(-1) >= 0 {
			status = http.StatusInternalServerError
		}
		aborted := false
		if p.DelayMS > 0 {
			timer := time.NewTimer(time.Duration(p.DelayMS) * time.Millisecond)
			select {
			case <-timer.C:
			case <-r.Context().Done():
				timer.Stop()
				aborted = true
			}
		}
		if !aborted {
			w.WriteHeader(status)
			w.Write([]byte("ok"))
		}
		write(map[string]any{"event": "request", "id": id, "phase": p.Name, "arrive_ns": arrive.UnixNano(),
			"done_ns": time.Now().UnixNano(), "arrive_mono_ns": arriveMono, "done_mono_ns": monoNow(), "status": status, "delay_ms": p.DelayMS, "aborted": aborted})
	})

	server := &http.Server{Handler: handler}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		write(map[string]any{"event": "stop", "t_ns": time.Now().UnixNano(), "mono_ns": monoNow()})
		server.Close()
	}()
	server.Serve(listener)
}
