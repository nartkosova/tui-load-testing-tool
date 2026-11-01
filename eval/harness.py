import argparse
import bisect
import csv
import datetime
import fcntl
import json
import os
import pty
import random
import re
import select
import signal
import socket
import sqlite3
import struct
import subprocess
import sys
import termios
import threading
import time
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
EVAL = REPO / "eval"
GEN_CPUS = "4-7"
TARGET_CPUS = "8-11"
GEN_CPU_COUNT = 4
TARGET_CPU_COUNT = 4
TARGET_PORT = 8099
TARGET_BASE = f"http://127.0.0.1:{TARGET_PORT}"
TARGET_NAME = "httpload-eval-target"
MONITOR_PORT = 8098
CLK = os.sysconf("SC_CLK_TCK")
PATTERNS = ["worker-pool", "fan-out-fan-in", "pipeline"]
REQUEST_TIMEOUT_S = 10
TUI_ROWS, TUI_COLS = 40, 120

RESULT_COLUMNS = [
    "run_id",
    "experiment_group",
    "repetition",
    "order_index",
    "timestamp_utc",
    "commit",
    "tool",
    "tool_version",
    "pattern",
    "endpoint",
    "concurrency",
    "wrk_threads",
    "tui_enabled",
    "sqlite_enabled",
    "configured_duration_s",
    "reported_duration_s",
    "wall_duration_s",
    "requests_attempted",
    "responses_completed",
    "execution_errors",
    "http_errors",
    "cancellations",
    "timeouts",
    "expiry_interrupted_inferred",
    "reported_rps",
    "completed_rps",
    "p50_ms",
    "p95_ms",
    "p99_ms",
    "max_ms",
    "latency_scope",
    "generator_cpu_user_s",
    "generator_cpu_system_s",
    "generator_cpu_util_pct_1cpu",
    "generator_cpu_util_pct_alloc",
    "generator_cpu_us_per_response",
    "generator_peak_rss_mib",
    "target_cpu_user_s",
    "target_cpu_system_s",
    "target_cpu_s",
    "target_cpu_util_pct_1cpu",
    "target_cpu_util_pct_alloc",
    "target_cpu_us_per_response",
    "target_peak_rss_mib",
    "tcp_active_opens",
    "tcp_attempt_fails",
    "time_wait_before",
    "time_wait_before_measured",
    "time_wait_after",
    "busy_pct_before",
    "cooldown_s",
    "warmup_exit_code",
    "active_window_s",
    "active_cpu_s",
    "active_rss_peak_mib",
    "tui_output_chunks",
    "tui_output_bytes",
    "tui_output_gap_p50_ms",
    "tui_output_gap_p99_ms",
    "tui_output_gap_max_ms",
    "tui_metric_updates",
    "tui_metric_gap_p50_ms",
    "tui_metric_gap_p95_ms",
    "tui_metric_gap_max_ms",
    "tui_first_update_ms",
    "cancel_response_ms",
    "final_summary_available",
    "post_output_exit_ms",
    "db_rows",
    "db_row_matches_summary",
    "exit_code",
    "status",
    "config_file",
    "raw_log",
    "notes",
]

RESOURCE_COLUMNS = [
    "run_id",
    "timestamp_utc",
    "t_rel_s",
    "role",
    "pid",
    "cpu_user_s_cum",
    "cpu_system_s_cum",
    "cpu_util_pct_1cpu",
    "rss_mib",
]


def utc_now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="milliseconds")


def na(value):
    if value is None:
        return "NA"
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, float):
        return f"{value:.6g}" if abs(value) < 1e-3 and value != 0 else f"{value:.6f}".rstrip("0").rstrip(".")
    return value


class CsvAppender:
    def __init__(self, path, columns):
        self.path = Path(path)
        self.columns = columns
        self.lock = threading.Lock()
        if not self.path.exists():
            with self.path.open("w", newline="") as f:
                csv.writer(f).writerow(columns)

    def write(self, rows):
        with self.lock, self.path.open("a", newline="") as f:
            writer = csv.writer(f)
            for row in rows:
                writer.writerow([na(row.get(c)) for c in self.columns])


def proc_cpu(pid):
    try:
        with open(f"/proc/{pid}/stat") as f:
            fields = f.read().rsplit(")", 1)[1].split()
        return int(fields[11]) / CLK, int(fields[12]) / CLK
    except (FileNotFoundError, ProcessLookupError, IndexError):
        return None


def proc_mem(pid):
    out = {}
    try:
        with open(f"/proc/{pid}/status") as f:
            for line in f:
                if line.startswith(("VmRSS:", "VmHWM:")):
                    key, value = line.split(":")
                    out[key] = int(value.split()[0]) / 1024
    except (FileNotFoundError, ProcessLookupError):
        return None
    return out


def tcp_snmp():
    with open("/proc/net/snmp") as f:
        lines = [line.split() for line in f if line.startswith("Tcp:")]
    return dict(zip(lines[0][1:], map(int, lines[1][1:])))


def time_wait_count():
    count = 0
    for name in ("/proc/net/tcp", "/proc/net/tcp6"):
        with open(name) as f:
            next(f)
            for line in f:
                if line.split(None, 4)[3] == "06":
                    count += 1
    return count


def cpu_busy_pct(interval=1.0):
    def read():
        with open("/proc/stat") as f:
            values = list(map(int, f.readline().split()[1:]))
        idle = values[3] + values[4]
        return sum(values), idle

    t0, i0 = read()
    time.sleep(interval)
    t1, i1 = read()
    return 100.0 * (1 - (i1 - i0) / max(1, t1 - t0))


class Sampler:
    def __init__(self, run_id, writer, interval=0.25):
        self.run_id = run_id
        self.writer = writer
        self.interval = interval
        self.pids = {}
        self.stop_event = threading.Event()
        self.rows = []
        self.thread = threading.Thread(target=self.loop, daemon=True)
        self.start_mono = time.monotonic()

    def add(self, role, pid):
        self.pids[role] = pid

    def start(self):
        self.thread.start()
        return self

    def loop(self):
        last = {}
        while not self.stop_event.is_set():
            now = time.monotonic()
            for role, pid in list(self.pids.items()):
                cpu = proc_cpu(pid)
                mem = proc_mem(pid)
                if cpu is None or mem is None:
                    continue
                util = None
                if role in last:
                    t_prev, c_prev = last[role]
                    if now > t_prev:
                        util = 100.0 * (sum(cpu) - c_prev) / (now - t_prev)
                last[role] = (now, sum(cpu))
                self.rows.append(
                    {
                        "run_id": self.run_id,
                        "timestamp_utc": utc_now(),
                        "t_rel_s": round(now - self.start_mono, 3),
                        "role": role,
                        "pid": pid,
                        "cpu_user_s_cum": cpu[0],
                        "cpu_system_s_cum": cpu[1],
                        "cpu_util_pct_1cpu": None if util is None else round(util, 2),
                        "rss_mib": round(mem.get("VmRSS", 0), 2),
                    }
                )
            self.stop_event.wait(self.interval)

    def stop(self):
        self.stop_event.set()
        self.thread.join()
        self.writer.write(self.rows)
        return self.rows


class Target:
    def __init__(self, image, log_dir):
        self.image = image
        self.log_dir = log_dir
        self.pid = None
        self.container = None

    def start(self):
        subprocess.run(["docker", "rm", "-f", TARGET_NAME], capture_output=True, timeout=30)
        if not wait_port_free(TARGET_PORT):
            raise RuntimeError(f"port {TARGET_PORT} is already in use by another process")
        out = subprocess.run(
            [
                "docker",
                "run",
                "-d",
                "--rm",
                "--name",
                TARGET_NAME,
                "--network",
                "host",
                "--cpuset-cpus",
                TARGET_CPUS,
                "--ulimit",
                "nofile=524288:524288",
                self.image,
            ],
            capture_output=True,
            text=True,
            timeout=60,
        )
        if out.returncode != 0:
            raise RuntimeError(f"docker run failed: {out.stderr}")
        self.container = out.stdout.strip()
        self.pid = int(
            subprocess.run(
                ["docker", "inspect", "-f", "{{.State.Pid}}", self.container],
                capture_output=True,
                text=True,
                timeout=30,
            ).stdout.strip()
        )
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            try:
                with urllib.request.urlopen(f"{TARGET_BASE}/fast", timeout=1) as r:
                    if r.status == 200:
                        running = subprocess.run(
                            ["docker", "inspect", "-f", "{{.State.Running}}", self.container],
                            capture_output=True,
                            text=True,
                            timeout=30,
                        ).stdout.strip()
                        if running != "true" or proc_cpu(self.pid) is None:
                            raise RuntimeError("target container is not the process answering")
                        return
            except OSError:
                time.sleep(0.1)
        raise RuntimeError("target did not become ready")

    def reset_hwm(self):
        try:
            with open(f"/proc/{self.pid}/clear_refs", "w") as f:
                f.write("5")
            return True
        except OSError:
            return False

    def snapshot(self):
        return proc_cpu(self.pid), proc_mem(self.pid)

    def stop(self):
        subprocess.run(["docker", "rm", "-f", TARGET_NAME], capture_output=True, timeout=60)
        self.pid = None


def run_process(argv, stdout_path, stderr_path, timeout_s, env=None, sampler=None, on_start=None):
    env = {**os.environ, **(env or {})}
    full = ["taskset", "-c", GEN_CPUS] + argv
    out_f = open(stdout_path, "wb")
    err_f = open(stderr_path, "wb")
    start_mono = time.monotonic()
    proc = subprocess.Popen(full, stdout=subprocess.PIPE, stderr=err_f, env=env)
    if sampler is not None:
        sampler.add("generator", proc.pid)
    state = {"last_out": None}

    def pump():
        while True:
            chunk = proc.stdout.read1(65536)
            if not chunk:
                break
            state["last_out"] = time.monotonic()
            out_f.write(chunk)

    pumper = threading.Thread(target=pump, daemon=True)
    pumper.start()
    if on_start is not None:
        on_start(proc)
    killed = {"value": False}

    def kill():
        killed["value"] = True
        try:
            os.kill(proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass

    timer = threading.Timer(timeout_s, kill)
    timer.start()
    _, status, usage = os.wait4(proc.pid, 0)
    end_mono = time.monotonic()
    timer.cancel()
    proc.returncode = os.waitstatus_to_exitcode(status)
    pumper.join(timeout=10)
    out_f.close()
    err_f.close()
    return {
        "exit_code": proc.returncode,
        "killed_by_timeout": killed["value"],
        "wall_s": end_mono - start_mono,
        "start_mono": start_mono,
        "end_mono": end_mono,
        "last_out_mono": state["last_out"],
        "cpu_user_s": usage.ru_utime,
        "cpu_system_s": usage.ru_stime,
        "peak_rss_mib": usage.ru_maxrss / 1024,
        "pid": proc.pid,
    }


ANSI = re.compile(rb"\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\x1b[()][A-Z0-9]|\x1b[=>]")
COUNT_RE = re.compile(rb"([0-9][0-9,]*)(?:/[0-9,]+)? requests")


def run_pty(argv, prefix, timeout_s, sampler=None, cancel_after_s=None):
    chunks = []
    buf = bytearray()
    events = {}
    cpu_marks = {}
    pid, fd = pty.fork()
    if pid == 0:
        env = {**os.environ, "TERM": "xterm-256color", "COLUMNS": str(TUI_COLS), "LINES": str(TUI_ROWS)}
        os.execvpe("taskset", ["taskset", "-c", GEN_CPUS] + argv, env)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", TUI_ROWS, TUI_COLS, 0, 0))
    start_mono = time.monotonic()
    if sampler is not None:
        sampler.add("generator", pid)
    phase = "form"
    deadline = start_mono + timeout_s
    search_from = 0
    killed = False
    while True:
        now = time.monotonic()
        if now > deadline and not killed:
            os.kill(pid, signal.SIGKILL)
            killed = True
        try:
            ready, _, _ = select.select([fd], [], [], 0.01)
        except InterruptedError:
            ready = []
        if ready:
            try:
                data = os.read(fd, 65536)
            except OSError:
                break
            if not data:
                break
            t = time.monotonic()
            chunks.append((t, len(buf), len(data)))
            buf.extend(data)
            tail = bytes(buf[-len(data) - 16 :])
            if b"\x1b]11;?" in tail and "osc11" not in events:
                os.write(fd, b"\x1b]11;rgb:0000/0000/0000\x1b\\")
                events["osc11"] = t
            if b"\x1b[6n" in tail and "dsr" not in events:
                os.write(fd, b"\x1b[1;1R")
                events["dsr"] = t
        now = time.monotonic()
        if phase == "form" and b"configure load test" in buf:
            os.write(fd, b"\r")
            events["enter"] = now
            cpu_marks["enter"] = (proc_cpu(pid), proc_mem(pid))
            search_from = len(buf)
            phase = "running"
        elif phase == "running":
            if cancel_after_s is not None and "cancel" not in events and now - events["enter"] >= cancel_after_s:
                os.write(fd, b"q")
                events["cancel"] = now
            if b"r new test" in buf[search_from:]:
                events["finished"] = chunks[-1][0]
                cpu_marks["finished"] = (proc_cpu(pid), proc_mem(pid))
                time.sleep(0.2)
                os.write(fd, b"q")
                events["quit"] = time.monotonic()
                phase = "quitting"
    _, status, usage = os.wait4(pid, 0)
    end_mono = time.monotonic()
    os.close(fd)
    Path(f"{prefix}.pty.bin").write_bytes(bytes(buf))
    with open(f"{prefix}.pty_chunks.csv", "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["t_rel_s", "offset", "length"])
        for t, off, n in chunks:
            w.writerow([f"{t - start_mono:.6f}", off, n])
    return {
        "exit_code": os.waitstatus_to_exitcode(status),
        "killed_by_timeout": killed,
        "wall_s": end_mono - start_mono,
        "start_mono": start_mono,
        "end_mono": end_mono,
        "cpu_user_s": usage.ru_utime,
        "cpu_system_s": usage.ru_stime,
        "peak_rss_mib": usage.ru_maxrss / 1024,
        "pid": pid,
        "buf": bytes(buf),
        "chunks": chunks,
        "events": events,
        "cpu_marks": cpu_marks,
    }


def pct(values, q):
    if not values:
        return None
    s = sorted(values)
    k = (len(s) - 1) * q
    lo = int(k)
    hi = min(lo + 1, len(s) - 1)
    return s[lo] + (s[hi] - s[lo]) * (k - lo)


def analyse_pty(res):
    ev = res["events"]
    out = {}
    if "enter" not in ev or "finished" not in ev:
        return out
    t0, t1 = ev["enter"], ev["finished"]
    times = [t for t, _, _ in res["chunks"] if t0 <= t <= t1]
    gaps = [(b - a) * 1000 for a, b in zip(times, times[1:])]
    out["tui_output_chunks"] = len(times)
    out["tui_output_bytes"] = sum(n for t, _, n in res["chunks"] if t0 <= t <= t1)
    out["tui_output_gap_p50_ms"] = pct(gaps, 0.5)
    out["tui_output_gap_p99_ms"] = pct(gaps, 0.99)
    out["tui_output_gap_max_ms"] = max(gaps) if gaps else None
    offsets = [off for _, off, _ in res["chunks"]]
    updates = []
    previous = None
    for m in COUNT_RE.finditer(res["buf"]):
        idx = bisect.bisect_right(offsets, m.end() - 1) - 1
        if idx < 0:
            continue
        t = res["chunks"][idx][0]
        if t < t0 or t > t1:
            continue
        value = int(m.group(1).replace(b",", b""))
        if value != previous:
            updates.append(t)
            previous = value
    ugaps = [(b - a) * 1000 for a, b in zip(updates, updates[1:])]
    out["tui_metric_updates"] = len(updates)
    out["tui_metric_gap_p50_ms"] = pct(ugaps, 0.5)
    out["tui_metric_gap_p95_ms"] = pct(ugaps, 0.95)
    out["tui_metric_gap_max_ms"] = max(ugaps) if ugaps else None
    out["tui_first_update_ms"] = (updates[0] - t0) * 1000 if updates else None
    out["active_window_s"] = t1 - t0
    a, b = res["cpu_marks"].get("enter"), res["cpu_marks"].get("finished")
    if a and b and a[0] and b[0]:
        out["active_cpu_s"] = sum(b[0]) - sum(a[0])
        out["active_rss_peak_mib"] = b[1].get("VmHWM")
    if "cancel" in ev:
        out["cancel_response_ms"] = (t1 - ev["cancel"]) * 1000
    return out


DUR_RE = re.compile(r"([0-9]+(?:\.[0-9]+)?)(ns|us|µs|μs|ms|s|m|h)")
DUR_UNITS = {"ns": 1e-6, "us": 1e-3, "µs": 1e-3, "μs": 1e-3, "ms": 1.0, "s": 1e3, "m": 6e4, "h": 3.6e6}


def go_duration_ms(text):
    parts = DUR_RE.findall(text)
    if not parts or "".join(a + b for a, b in parts) != text:
        raise ValueError(f"bad duration {text!r}")
    return sum(float(a) * DUR_UNITS[b] for a, b in parts)


def parse_httpload(text):
    def field(name):
        m = re.search(rf"^{re.escape(name)}: (.+)$", text, re.M)
        return m.group(1).strip() if m else None

    req = field("requests")
    if req is None:
        raise ValueError("summary not found")
    out = {
        "requests_attempted": int(req),
        "responses_completed": int(field("responses")),
        "execution_errors": int(field("errors")),
        "http_errors": int(field("HTTP errors")),
        "cancellations": int(field("canceled")),
        "reported_duration_s": go_duration_ms(field("duration")) / 1000,
    }
    rps = field("requests/sec")
    out["reported_rps"] = None if rps == "unavailable" else float(rps)
    lat = field("success latency")
    if lat and not lat.startswith("unavailable"):
        kv = dict(item.split("=", 1) for item in lat.split())
        for key, col in (("p50", "p50_ms"), ("p95", "p95_ms"), ("p99", "p99_ms"), ("max", "max_ms")):
            out[col] = go_duration_ms(kv[key])
        out["success_samples"] = int(kv["samples"])
        out["success_rejected"] = int(kv["rejected"])
    fail = field("failure latency")
    if fail and not fail.startswith("unavailable"):
        kv = dict(item.split("=", 1) for item in fail.split())
        out["failure_max_ms"] = go_duration_ms(kv["max"])
        out["failure_samples"] = int(kv["samples"])
    out["latency_scope"] = "success responses (status<400), request creation to body fully read, incl. connection setup"
    return out


def parse_wrk(text):
    m = re.search(r"^WRKJSON (\{.*\})$", text, re.M)
    if not m:
        raise ValueError("wrk JSON not found")
    d = json.loads(m.group(1))
    dur = d["duration_us"] / 1e6
    return {
        "requests_attempted": None,
        "responses_completed": d["requests"],
        "execution_errors": d["connect"] + d["read"] + d["write"] + d["timeout"],
        "http_errors": d["status"],
        "timeouts": d["timeout"],
        "cancellations": None,
        "reported_duration_s": dur,
        "reported_rps": d["requests"] / dur if dur else None,
        "p50_ms": d["p50_us"] / 1000,
        "p95_ms": d["p95_us"] / 1000,
        "p99_ms": d["p99_us"] / 1000,
        "max_ms": d["max_us"] / 1000,
        "latency_scope": "all completed responses, request write to response complete on an established keep-alive connection",
        "wrk_errors": f"connect={d['connect']};read={d['read']};write={d['write']};status={d['status']};timeout={d['timeout']}",
    }


def parse_hey(text):
    def num(pattern):
        m = re.search(pattern, text, re.M)
        return float(m.group(1)) if m else None

    total = num(r"^\s*Total:\s+([0-9.]+) secs")
    if total is None:
        raise ValueError("hey summary not found")
    statuses = {int(a): int(b) for a, b in re.findall(r"^\s*\[(\d+)\]\s+(\d+) responses", text, re.M)}
    err_section = text.split("Error distribution:", 1)
    errors = 0
    if len(err_section) == 2:
        errors = sum(int(n) for n in re.findall(r"^\s*\[(\d+)\]\s", err_section[1], re.M))
    responses = sum(statuses.values())
    out = {
        "requests_attempted": responses + errors,
        "responses_completed": responses,
        "execution_errors": errors,
        "http_errors": sum(v for k, v in statuses.items() if k >= 400),
        "cancellations": None,
        "reported_duration_s": total,
        "reported_rps": num(r"^\s*Requests/sec:\s+([0-9.]+)"),
        "max_ms": (num(r"^\s*Slowest:\s+([0-9.]+) secs") or 0) * 1000,
        "latency_scope": "all non-error responses, request start to body read, incl. connection setup (hey index convention)",
    }
    for p, col in (("50", "p50_ms"), ("95", "p95_ms"), ("99", "p99_ms")):
        v = num(rf"^\s*{p}%+ in ([0-9.]+) secs")
        out[col] = None if v is None else v * 1000
    return out


def parse_k6(summary_path):
    d = json.loads(Path(summary_path).read_text())
    metrics = d["metrics"]

    def count(name):
        m = metrics.get(name)
        return None if m is None else m["values"].get("count")

    reqs = count("http_reqs")
    failed_exec = count("http_reqs{status:0}") or 0
    unexpected = count("http_reqs{expected_response:false}") or 0
    lat = metrics.get("http_req_duration{expected_response:true}", {}).get("values", {})
    dur = d["state"]["testRunDurationMs"] / 1000
    out = {
        "requests_attempted": reqs,
        "responses_completed": None if reqs is None else reqs - failed_exec,
        "execution_errors": failed_exec,
        "http_errors": unexpected - failed_exec,
        "cancellations": None,
        "reported_duration_s": dur,
        "reported_rps": metrics["http_reqs"]["values"].get("rate"),
        "p50_ms": lat.get("med"),
        "p95_ms": lat.get("p(95)"),
        "p99_ms": lat.get("p(99)"),
        "max_ms": lat.get("max"),
        "latency_scope": "http_req_duration{expected_response:true}: sending+waiting+receiving, excludes blocked/connect time",
    }
    iters = metrics.get("iterations", {}).get("values", {}).get("count")
    out["k6_iterations"] = iters
    return out


class Evaluation:
    def __init__(self, run_dir, pilot=False):
        self.run_dir = Path(run_dir).resolve()
        self.base = self.run_dir / "pilot" if pilot else self.run_dir
        self.base.mkdir(parents=True, exist_ok=True)
        self.raw = self.base / "raw"
        (self.raw / "runs").mkdir(parents=True, exist_ok=True)
        (self.raw / "configs").mkdir(parents=True, exist_ok=True)
        self.bin = self.run_dir / "bin"
        self.tools = self.run_dir / "tools"
        self.env = json.loads((self.run_dir / "environment.json").read_text())
        self.commit = self.env["repository"]["commit"]
        self.versions = self.env["tool_versions"]
        self.results = CsvAppender(self.base / "results.csv", RESULT_COLUMNS)
        self.resources = CsvAppender(self.base / "resources.csv", RESOURCE_COLUMNS)
        self.target = Target(self.env["target"]["image"], self.raw)
        self.baseline_busy = self.env["baseline"]["busy_pct_10s"]
        self.pilot = pilot

    def rel(self, path):
        return str(Path(path).relative_to(self.run_dir))

    def command(self, tool, endpoint, concurrency, duration_s, prefix, sqlite_path=None):
        url = f"{TARGET_BASE}/{endpoint}"
        extra = {}
        env = {}
        if tool == "httpload":
            argv = [
                str(self.bin / "httpload"),
                "-url",
                url,
                "-pattern",
                extra.get("pattern", "worker-pool"),
                "-concurrency",
                str(concurrency),
                "-duration",
                f"{duration_s}s",
                "-timeout",
                f"{REQUEST_TIMEOUT_S}s",
            ]
            if sqlite_path:
                argv += ["-db", str(sqlite_path)]
        elif tool == "k6":
            summary = f"{prefix}.k6summary.json"
            argv = [
                str(self.tools / "k6"),
                "run",
                "--quiet",
                "--no-color",
                "--no-usage-report",
                str(EVAL / "k6_closed_loop.js"),
            ]
            env = {
                "URL": url,
                "VUS": str(concurrency),
                "DURATION": f"{duration_s}s",
                "TIMEOUT": f"{REQUEST_TIMEOUT_S}s",
                "SUMMARY": summary,
                "K6_NO_USAGE_REPORT": "true",
            }
            extra["summary"] = summary
        elif tool == "wrk":
            threads = min(GEN_CPU_COUNT, concurrency)
            argv = [
                "wrk",
                "-t",
                str(threads),
                "-c",
                str(concurrency),
                "-d",
                f"{duration_s}s",
                "--timeout",
                f"{REQUEST_TIMEOUT_S}s",
                "-s",
                str(EVAL / "wrk_report.lua"),
                url,
            ]
            extra["wrk_threads"] = threads
        elif tool == "hey":
            argv = ["hey", "-z", f"{duration_s}s", "-c", str(concurrency), "-t", str(REQUEST_TIMEOUT_S), url]
        else:
            raise ValueError(tool)
        return argv, env, extra

    def parse(self, tool, stdout_text, extra):
        if tool == "httpload":
            return parse_httpload(stdout_text)
        if tool == "wrk":
            return parse_wrk(stdout_text)
        if tool == "hey":
            return parse_hey(stdout_text)
        if tool == "k6":
            return parse_k6(extra["summary"])
        raise ValueError(tool)

    def cooldown(self, minimum=3.0):
        start = time.monotonic()
        time.sleep(minimum)
        busy = cpu_busy_pct(1.0)
        while busy > self.baseline_busy + 10 and time.monotonic() - start < 30:
            time.sleep(2)
            busy = cpu_busy_pct(1.0)
        return time.monotonic() - start

    def http_run(self, spec, warmup_s, measure_s):
        run_id = spec["run_id"]
        prefix = self.raw / "runs" / run_id
        tool, endpoint, conc = spec["tool"], spec["endpoint"], spec["concurrency"]
        pattern = spec.get("pattern")
        tui = spec.get("tui", False)
        sqlite_path = (self.raw / "runs" / f"{run_id}.db") if spec.get("sqlite") else None
        row = {
            "run_id": run_id,
            "experiment_group": spec["group"],
            "repetition": spec["rep"],
            "order_index": spec["order"],
            "timestamp_utc": utc_now(),
            "commit": self.commit,
            "tool": tool,
            "tool_version": self.versions[tool],
            "pattern": pattern if tool == "httpload" else None,
            "endpoint": f"/{endpoint}",
            "concurrency": conc,
            "tui_enabled": tui,
            "sqlite_enabled": bool(sqlite_path),
            "configured_duration_s": measure_s,
            "raw_log": self.rel(f"{prefix}.stdout"),
        }
        notes = []
        offset0 = wall_offset()
        row["time_wait_before"] = time_wait_count()
        row["busy_pct_before"] = round(cpu_busy_pct(0.5), 2)
        try:
            self.target.start()
        except Exception as exc:
            row.update(status="skipped", notes=f"target start failed: {exc}")
            self.results.write([row])
            return row

        def build(duration, pfx, db=None):
            argv, env, extra = self.command(tool, endpoint, conc, duration, pfx, db)
            if tool == "httpload":
                argv[argv.index("-pattern") + 1] = pattern
                if tui:
                    argv.insert(1, "-tui")
            return argv, env, extra

        wargv, wenv, wextra = build(warmup_s, f"{prefix}.warmup")
        if tui:
            wres = run_pty(wargv, f"{prefix}.warmup", warmup_s + 60)
        else:
            wres = run_process(wargv, f"{prefix}.warmup.stdout", f"{prefix}.warmup.stderr", warmup_s + 60, wenv)
        row["warmup_exit_code"] = wres["exit_code"]
        if wres["exit_code"] != 0:
            notes.append(f"warm-up exit {wres['exit_code']}")

        argv, env, extra = build(measure_s, str(prefix), sqlite_path)
        if "wrk_threads" in extra:
            row["wrk_threads"] = extra["wrk_threads"]
        config = {
            "run_id": run_id,
            "spec": spec,
            "argv": ["taskset", "-c", GEN_CPUS] + argv,
            "env": env,
            "warmup_argv": ["taskset", "-c", GEN_CPUS] + wargv,
            "warmup_s": warmup_s,
            "measure_s": measure_s,
            "target": {
                "image": self.target.image,
                "cpuset": TARGET_CPUS,
                "network": "host",
                "url": f"{TARGET_BASE}/{endpoint}",
            },
            "pty": {"rows": TUI_ROWS, "cols": TUI_COLS} if tui else None,
        }
        config_path = self.raw / "configs" / f"{run_id}.json"
        config_path.write_text(json.dumps(config, indent=2))
        row["config_file"] = self.rel(config_path)

        row["time_wait_before_measured"] = time_wait_count()
        hwm_reset = self.target.reset_hwm()
        if not hwm_reset:
            notes.append("target HWM reset failed; target peak RSS includes warm-up")
        t_cpu0, _ = self.target.snapshot()
        snmp0 = tcp_snmp()
        sampler = Sampler(run_id, self.resources)
        sampler.add("target", self.target.pid)
        sampler.start()
        cancel_info = {}

        def cli_cancel(proc):
            if spec.get("cancel_after_s") is None:
                return

            def fire():
                cancel_info["t"] = time.monotonic()
                try:
                    os.kill(proc.pid, signal.SIGINT)
                except ProcessLookupError:
                    pass

            threading.Timer(spec["cancel_after_s"], fire).start()

        if tui:
            res = run_pty(argv, str(prefix), measure_s + 90, sampler, spec.get("cancel_after_s"))
            Path(f"{prefix}.stdout").write_bytes(ANSI.sub(b"", res["buf"]))
            row["raw_log"] = self.rel(f"{prefix}.pty.bin")
        else:
            res = run_process(
                argv, f"{prefix}.stdout", f"{prefix}.stderr", measure_s + 90, env, sampler, on_start=cli_cancel
            )
        sampler.stop()
        t_cpu1, t_mem1 = self.target.snapshot()
        snmp1 = tcp_snmp()
        self.target.stop()
        row["time_wait_after"] = time_wait_count()
        row["tcp_active_opens"] = snmp1["ActiveOpens"] - snmp0["ActiveOpens"]
        row["tcp_attempt_fails"] = snmp1["AttemptFails"] - snmp0["AttemptFails"]
        row["exit_code"] = res["exit_code"]
        row["wall_duration_s"] = res["wall_s"]
        row["generator_cpu_user_s"] = res["cpu_user_s"]
        row["generator_cpu_system_s"] = res["cpu_system_s"]
        row["generator_peak_rss_mib"] = res["peak_rss_mib"]
        gen_cpu = res["cpu_user_s"] + res["cpu_system_s"]
        row["generator_cpu_util_pct_1cpu"] = 100 * gen_cpu / res["wall_s"]
        row["generator_cpu_util_pct_alloc"] = 100 * gen_cpu / res["wall_s"] / GEN_CPU_COUNT
        if t_cpu0 and t_cpu1:
            row["target_cpu_user_s"] = t_cpu1[0] - t_cpu0[0]
            row["target_cpu_system_s"] = t_cpu1[1] - t_cpu0[1]
            row["target_cpu_s"] = row["target_cpu_user_s"] + row["target_cpu_system_s"]
            row["target_cpu_util_pct_1cpu"] = 100 * row["target_cpu_s"] / res["wall_s"]
            row["target_cpu_util_pct_alloc"] = row["target_cpu_util_pct_1cpu"] / TARGET_CPU_COUNT
        if t_mem1:
            row["target_peak_rss_mib"] = t_mem1.get("VmHWM")
        else:
            notes.append("target process not readable after run")
        if res.get("killed_by_timeout"):
            notes.append("killed by harness timeout")
        if res.get("last_out_mono"):
            row["post_output_exit_ms"] = (res["end_mono"] - res["last_out_mono"]) * 1000
        if "t" in cancel_info:
            row["cancel_response_ms"] = (res["end_mono"] - cancel_info["t"]) * 1000
        if tui:
            row.update(analyse_pty(res))
            if "finished" not in res["events"]:
                notes.append("TUI finished screen not detected")
        text = Path(f"{prefix}.stdout").read_text(errors="replace")
        try:
            parsed = self.parse(tool, text, extra)
            for key in RESULT_COLUMNS:
                if key in parsed:
                    row[key] = parsed[key]
            parse_ok = True
        except (ValueError, KeyError, FileNotFoundError, json.JSONDecodeError) as exc:
            parsed = {}
            parse_ok = False
            notes.append(f"parse failed: {exc}")
        if spec.get("cancel_after_s") is not None:
            row["final_summary_available"] = parse_ok
        if tool == "httpload" and parse_ok:
            row["timeouts"] = None
            fmax = parsed.get("failure_max_ms")
            errs = parsed["execution_errors"] - parsed["cancellations"]
            if errs == 0:
                row["expiry_interrupted_inferred"] = 0
            elif errs <= conc and fmax is not None and fmax < REQUEST_TIMEOUT_S * 1000:
                row["expiry_interrupted_inferred"] = errs
            if "failure_max_ms" in parsed:
                notes.append(
                    f"failure latency samples={parsed['failure_samples']} max={parsed['failure_max_ms']:.3f}ms"
                )
        if tool == "wrk" and parse_ok:
            notes.append(parsed["wrk_errors"])
        if tool == "k6" and parse_ok:
            notes.append(f"k6 iterations={parsed.get('k6_iterations')}")
        if parse_ok and row.get("responses_completed") and row.get("reported_duration_s"):
            row["completed_rps"] = row["responses_completed"] / row["reported_duration_s"]
            row["generator_cpu_us_per_response"] = gen_cpu / row["responses_completed"] * 1e6
            if row.get("target_cpu_s") is not None:
                row["target_cpu_us_per_response"] = row["target_cpu_s"] / row["responses_completed"] * 1e6
        if sqlite_path is not None:
            row.update(self.check_load_db(sqlite_path, parsed))
        if row.get("target_cpu_util_pct_alloc") and row["target_cpu_util_pct_alloc"] > 90:
            notes.append("target CPU >90% of its 4 CPUs (target-saturated)")
        if row.get("generator_cpu_util_pct_alloc") and row["generator_cpu_util_pct_alloc"] > 90:
            notes.append("generator CPU >90% of its 4 CPUs (generator-saturated)")
        expected_exit = 1 if spec.get("cancel_after_s") is not None and tool == "httpload" else 0
        if not parse_ok:
            row["status"] = "failed"
        elif res["exit_code"] != expected_exit:
            row["status"] = "failed"
            notes.append(f"unexpected exit code (expected {expected_exit})")
        else:
            row["status"] = "ok"
        step_ms = (wall_offset() - offset0) / 1e6
        if abs(step_ms) > 20:
            notes.append(f"wall clock stepped {step_ms:+.1f} ms during run (durations use monotonic clocks)")
        row["cooldown_s"] = self.cooldown()
        row["notes"] = "; ".join(notes) if notes else None
        self.results.write([row])
        return row

    def check_load_db(self, path, parsed):
        out = {}
        try:
            con = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
            rows = con.execute(
                "SELECT requests, responses, errors, http_errors, canceled, duration, p50, p99 FROM load_runs"
            ).fetchall()
            con.close()
        except sqlite3.Error as exc:
            return {"db_rows": 0, "db_row_matches_summary": False, "notes": f"db read failed: {exc}"}
        out["db_rows"] = len(rows)
        if len(rows) == 1 and parsed:
            r = rows[0]
            out["db_row_matches_summary"] = (
                r[0] == parsed["requests_attempted"]
                and r[1] == parsed["responses_completed"]
                and r[2] == parsed["execution_errors"]
                and r[3] == parsed["http_errors"]
                and r[4] == parsed["cancellations"]
                and abs(r[5] / 1e9 - parsed["reported_duration_s"]) < 1e-3
            )
        else:
            out["db_row_matches_summary"] = False
        return out


def plan(groups, reps, seed):
    specs = []
    for group in groups:
        configs = []
        if group == "A_patterns":
            for p in PATTERNS:
                for e in ("fast", "slow"):
                    for c in (10, 100, 1000):
                        configs.append({"tool": "httpload", "pattern": p, "endpoint": e, "concurrency": c})
        elif group == "B_tools":
            for t in ("k6", "wrk", "hey"):
                for e in ("fast", "slow"):
                    for c in (100, 1000):
                        configs.append({"tool": t, "endpoint": e, "concurrency": c})
        elif group == "B2_hey5s":
            for c in (100, 1000):
                configs.append({"tool": "hey", "endpoint": "fast", "concurrency": c})
        elif group == "C_tui":
            for p in PATTERNS:
                for c in (100, 1000):
                    configs.append(
                        {"tool": "httpload", "pattern": p, "endpoint": "fast", "concurrency": c, "tui": True}
                    )
        elif group == "D_cancel":
            for p in PATTERNS:
                for c in (1000,):
                    for tui in (False, True):
                        configs.append(
                            {
                                "tool": "httpload",
                                "pattern": p,
                                "endpoint": "fast",
                                "concurrency": c,
                                "tui": tui,
                                "cancel_after_s": 5,
                            }
                        )
        elif group == "E_sqlite":
            for c in (100, 1000):
                for db in (False, True):
                    configs.append(
                        {
                            "tool": "httpload",
                            "pattern": "worker-pool",
                            "endpoint": "fast",
                            "concurrency": c,
                            "sqlite": db,
                        }
                    )
        group_reps = min(2, reps) if group == "D_cancel" else reps
        items = [dict(cfg, group=group, rep=r + 1) for cfg in configs for r in range(group_reps)]
        rng = random.Random(f"{seed}-{group}")
        rng.shuffle(items)
        specs.extend(items)
    for i, s in enumerate(specs):
        s["order"] = i
        name = s["tool"] if s["tool"] != "httpload" else s["pattern"]
        flags = ("-tui" if s.get("tui") else "") + ("-db" if s.get("sqlite") else "")
        s["run_id"] = f"{s['group'].split('_')[0]}{i:03d}-{name}-{s['endpoint']}-c{s['concurrency']}{flags}-r{s['rep']}"
    return specs


def cmd_http(args):
    ev = Evaluation(args.run_dir, args.pilot)
    groups = args.groups.split(",")
    specs = plan(groups, args.reps, args.seed)
    if args.pilot:
        seen = set()
        pilot_specs = []
        for s in specs:
            key = (s["group"], s["tool"], s.get("tui", False), s.get("sqlite", False), s.get("cancel_after_s"))
            if key not in seen:
                seen.add(key)
                pilot_specs.append(s)
        specs = pilot_specs
    done = set()
    results_path = ev.base / "results.csv"
    if results_path.exists():
        with results_path.open() as f:
            done = {r["run_id"] for r in csv.DictReader(f)}
    (ev.base / f"plan_{'_'.join(groups)}.json").write_text(
        json.dumps({"seed": args.seed, "reps": args.reps, "specs": specs}, indent=1)
    )
    started = time.monotonic()
    for i, spec in enumerate(specs):
        if spec["run_id"] in done:
            continue
        measure = spec.get("cancel_after_s") and 30 or args.measure
        t0 = time.monotonic()
        row = ev.http_run(spec, args.warmup, measure)
        print(
            f"[{i + 1}/{len(specs)}] {spec['run_id']} status={row.get('status')} exit={row.get('exit_code')} "
            f"rps={row.get('completed_rps')} errs={row.get('execution_errors')} took={time.monotonic() - t0:.1f}s "
            f"elapsed={(time.monotonic() - started) / 60:.1f}min",
            flush=True,
        )


def wait_port_free(port, timeout=10):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        with socket.socket() as s:
            if s.connect_ex(("127.0.0.1", port)) != 0:
                return True
        time.sleep(0.2)
    return False


MONITOR_SCHEDULE = [
    {"name": "healthy", "at_s": 0},
    {"name": "isolated_failure", "at_s": 10, "fail_next": 1},
    {"name": "sustained_failure", "at_s": 16, "status": 503},
    {"name": "recovery", "at_s": 28},
    {"name": "slow", "at_s": 36, "delay_ms": 350},
    {"name": "healthy_after_slow", "at_s": 44},
    {"name": "timeout", "at_s": 50, "delay_ms": 1500},
    {"name": "healthy_after_timeout", "at_s": 58},
    {"name": "inflight_at_shutdown", "at_s": 64, "delay_ms": 600},
]
MONITOR_ARGS = {"interval": "1s", "timeout": "800ms", "fail_threshold": 2, "degraded": "200ms"}
SHUTDOWN_AFTER_S = 66


def start_monitor_server(ev, schedule, prefix):
    sched_path = f"{prefix}.schedule.json"
    Path(sched_path).write_text(json.dumps(schedule))
    log_path = f"{prefix}.server.jsonl"
    if not wait_port_free(MONITOR_PORT):
        raise RuntimeError("monitor port busy")
    proc = subprocess.Popen(
        [
            "taskset",
            "-c",
            TARGET_CPUS,
            str(ev.bin / "monitorserver"),
            "-addr",
            f"127.0.0.1:{MONITOR_PORT}",
            "-schedule",
            sched_path,
            "-log",
            log_path,
        ],
        stdout=open(f"{prefix}.server.stdout", "wb"),
        stderr=open(f"{prefix}.server.stderr", "wb"),
    )
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if Path(log_path).exists() and '"event":"start"' in Path(log_path).read_text():
            return proc, log_path
        time.sleep(0.02)
    proc.kill()
    raise RuntimeError("monitor server not ready")


def read_jsonl(path):
    events = [json.loads(line) for line in Path(path).read_text().splitlines() if line.strip()]
    for e in events:
        if "mono_ns" in e:
            e["wall_ns"] = e["t_ns"]
            e["t_ns"] = e["mono_ns"]
        if "arrive_mono_ns" in e:
            e["arrive_wall_ns"] = e["arrive_ns"]
            e["arrive_ns"] = e["arrive_mono_ns"]
            e["done_ns"] = e["done_mono_ns"]
    return events


WALL_OFFSET = [0]


def wall_offset():
    return time.time_ns() - time.monotonic_ns()


class MonitorProcess:
    def __init__(self, ev, prefix, db_path, sampler):
        self.lines = []
        argv = [
            "taskset",
            "-c",
            GEN_CPUS,
            str(ev.bin / "httpload"),
            "monitor",
            "-url",
            f"http://127.0.0.1:{MONITOR_PORT}/health",
            "-interval",
            MONITOR_ARGS["interval"],
            "-timeout",
            MONITOR_ARGS["timeout"],
            "-fail-threshold",
            str(MONITOR_ARGS["fail_threshold"]),
            "-degraded",
            MONITOR_ARGS["degraded"],
            "-v",
        ]
        if db_path:
            argv += ["-db", str(db_path)]
        self.argv = argv
        self.stdout_path = f"{prefix}.stdout"
        self.start_mono = time.monotonic()
        self.start_ns = time.monotonic_ns()
        self.proc = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=open(f"{prefix}.stderr", "wb"))
        if sampler:
            sampler.add("generator", self.proc.pid)
        self.out = open(self.stdout_path, "w")
        self.thread = threading.Thread(target=self.pump, daemon=True)
        self.thread.start()

    def pump(self):
        for raw in iter(self.proc.stdout.readline, b""):
            t_ns = time.monotonic_ns()
            line = raw.decode(errors="replace").rstrip("\n")
            self.lines.append((t_ns, line))
            self.out.write(f"{t_ns}\t{line}\n")
            self.out.flush()

    def stop(self):
        self.signal_ns = time.monotonic_ns()
        self.proc.send_signal(signal.SIGINT)
        _, status, usage = os.wait4(self.proc.pid, 0)
        self.end_ns = time.monotonic_ns()
        self.proc.returncode = os.waitstatus_to_exitcode(status)
        self.thread.join(timeout=5)
        self.out.close()
        self.usage = usage
        return self.proc.returncode


LINE_RE = re.compile(r"^(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d)  (\S+)  (.*)$")


def parse_monitor_lines(lines):
    checks, transitions = [], []
    summary = {}
    in_summary = False
    for t_ns, line in lines:
        m = LINE_RE.match(line)
        if m and not in_summary:
            rest = m.group(3)
            checks.append((t_ns, rest))
            if "→" in rest:
                tm = re.match(r"(\S+) → (\S+)", rest)
                transitions.append({"t_ns": t_ns, "from": tm.group(1), "to": tm.group(2), "line": line})
            continue
        if line.startswith("state: "):
            in_summary = True
            summary["state"] = line.split(": ", 1)[1]
        for key in ("checks", "failed checks", "incidents"):
            if line.startswith(f"{key}: "):
                summary[key] = int(line.split(": ", 1)[1])
        if line.startswith("uptime: "):
            mu = re.match(r"uptime: ([0-9.]+)% over (\S+)", line)
            summary["uptime_pct"] = float(mu.group(1))
            summary["observed"] = mu.group(2)
        if line.strip().startswith("down "):
            summary.setdefault("incident_lines", []).append(line.strip())
    return checks, transitions, summary


def policy_reference(requests, threshold, degraded_ms, timeout_ms, end_ns):
    state = "UNKNOWN"
    streak = 0
    streak_at = None
    transitions, incidents = [], []
    first = None
    for r in requests:
        at = r["arrive_ns"]
        if first is None:
            first = at
        latency_ms = (r["done_ns"] - r["arrive_ns"]) / 1e6
        ok = (not r["aborted"]) and 200 <= r["status"] < 400 and latency_ms < timeout_ms
        if not ok:
            if streak == 0:
                streak_at = at
            streak += 1
            if streak >= threshold and state != "DOWN":
                transitions.append({"at_ns": at, "from": state, "to": "DOWN", "request_id": r["id"]})
                state = "DOWN"
                incidents.append([streak_at, None])
            continue
        streak = 0
        if state == "DOWN":
            incidents[-1][1] = at
        nxt = "DEGRADED" if latency_ms > degraded_ms else "UP"
        if nxt != state:
            transitions.append({"at_ns": at, "from": state, "to": nxt, "request_id": r["id"]})
            state = nxt
    down = sum(((e or end_ns) - s) for s, e in incidents)
    uptime = 100 * (end_ns - first - down) / (end_ns - first) if first else None
    return transitions, incidents, uptime


def cmd_monitor(args):
    ev = Evaluation(args.run_dir, args.pilot)
    events_out = CsvAppender(
        ev.base / "monitor_events.csv",
        [
            "run_id",
            "repetition",
            "sqlite_enabled",
            "event_kind",
            "phase",
            "expected_transition",
            "server_change_utc",
            "server_change_rel_s",
            "policy_check_utc",
            "observed_report_utc",
            "observed_transition",
            "detection_delay_s",
            "delay_from_policy_check_s",
            "reported_backdated_start_utc",
            "value_reference",
            "value_observed",
            "difference",
            "notes",
        ],
    )
    reps = 1 if args.pilot else args.reps
    order = [(db, r + 1) for db in (False, True) for r in range(reps)]
    random.Random(f"{args.seed}-monitor").shuffle(order)
    for i, (db, rep) in enumerate(order):
        run_id = f"M{i:03d}-monitor{'-db' if db else ''}-r{rep}"
        prefix = ev.raw / "runs" / run_id
        db_path = ev.raw / "runs" / f"{run_id}.db" if db else None
        t0 = time.monotonic()
        row = monitor_run(ev, events_out, run_id, rep, prefix, db_path, i)
        print(f"[{i + 1}/{len(order)}] {run_id} status={row['status']} took={time.monotonic() - t0:.1f}s", flush=True)
        time.sleep(3)
    if not args.skip_restart:
        restart_scenario(ev, events_out)


def iso_ns(ns):
    if ns is None:
        return None
    return datetime.datetime.fromtimestamp((ns + WALL_OFFSET[0]) / 1e9, datetime.timezone.utc).isoformat(
        timespec="microseconds"
    )


def monitor_run(ev, events_out, run_id, rep, prefix, db_path, order):
    WALL_OFFSET[0] = wall_offset()
    server, log_path = start_monitor_server(ev, MONITOR_SCHEDULE, prefix)
    sampler = Sampler(run_id, ev.resources).start()
    sampler.add("target", server.pid)
    mon = MonitorProcess(ev, prefix, db_path, sampler)
    server_start = read_jsonl(log_path)[0]["t_ns"]
    shutdown_deadline = server_start + SHUTDOWN_AFTER_S * 1e9
    while time.monotonic_ns() < shutdown_deadline:
        time.sleep(0.05)
    inflight_id = None
    probe_start = time.monotonic_ns()
    while time.monotonic_ns() - probe_start < 3e9:
        arrivals = [
            e
            for e in read_jsonl(log_path)
            if e["event"] == "arrive" and e["phase"] == "inflight_at_shutdown" and e["t_ns"] >= probe_start
        ]
        if arrivals:
            inflight_id = arrivals[-1]["id"]
            time.sleep(0.25)
            break
        time.sleep(0.005)
    exit_code = mon.stop()
    sampler.stop()
    time.sleep(0.3)
    server.send_signal(signal.SIGTERM)
    server.wait(timeout=10)
    Path(f"{prefix}.inflight_probe.txt").write_text(
        f"inflight_request_expected_id={inflight_id}\nsigint_ns={mon.signal_ns}\n"
    )
    events = read_jsonl(log_path)
    phases = [e for e in events if e["event"] == "phase"]
    requests = sorted(
        [e for e in events if e["event"] == "request" and e["arrive_ns"] < mon.signal_ns], key=lambda e: e["arrive_ns"]
    )
    inflight = [r for r in requests if r["done_ns"] > mon.signal_ns]
    completed = [r for r in requests if r not in inflight]
    checks, transitions, summary = parse_monitor_lines(mon.lines)
    ref_transitions, ref_incidents, ref_uptime = policy_reference(
        completed, MONITOR_ARGS["fail_threshold"], 200, 800, mon.signal_ns
    )
    phase_at = {p["name"]: p for p in phases}
    rows = []
    expectations = [
        ("detect_down", "sustained_failure", "UP→DOWN"),
        ("recover", "recovery", "DOWN→UP"),
        ("detect_degraded", "slow", "UP→DEGRADED"),
        ("recover_degraded", "healthy_after_slow", "DEGRADED→UP"),
        ("detect_down", "timeout", "UP→DOWN"),
        ("recover", "healthy_after_timeout", "DOWN→UP"),
        ("detect_degraded", "inflight_at_shutdown", "UP→DEGRADED"),
    ]
    used = set()
    for kind, phase, expected in expectations:
        p = phase_at.get(phase)
        if p is None:
            continue
        frm, to = expected.split("→")
        obs = next(
            (
                (j, t)
                for j, t in enumerate(transitions)
                if j not in used and t["t_ns"] >= p["t_ns"] and t["from"] == frm and t["to"] == to
            ),
            None,
        )
        refm = next(
            (t for t in ref_transitions if t["at_ns"] >= p["t_ns"] and t["from"] == frm and t["to"] == to), None
        )
        note = None
        if obs is not None:
            used.add(obs[0])
            obs = obs[1]
        else:
            note = "transition not observed before shutdown"
        backdated = None
        if obs is not None and to == "DOWN":
            m = re.search(r"since (\d{4}-\d\d-\d\d \d\d:\d\d:\d\d)", obs["line"])
            backdated = m.group(1) + " (local, 1 s resolution)" if m else None
        rows.append(
            {
                "run_id": run_id,
                "repetition": rep,
                "sqlite_enabled": bool(db_path),
                "event_kind": kind,
                "phase": phase,
                "expected_transition": expected,
                "server_change_utc": iso_ns(p["t_ns"]),
                "server_change_rel_s": round(p["rel_s"], 4),
                "policy_check_utc": iso_ns(refm["at_ns"]) if refm else None,
                "observed_report_utc": iso_ns(obs["t_ns"]) if obs else None,
                "observed_transition": f"{obs['from']}→{obs['to']}" if obs else None,
                "detection_delay_s": (obs["t_ns"] - p["t_ns"]) / 1e9 if obs else None,
                "delay_from_policy_check_s": (obs["t_ns"] - refm["at_ns"]) / 1e9 if obs and refm else None,
                "reported_backdated_start_utc": backdated,
                "notes": note,
            }
        )
    iso = phase_at["isolated_failure"]
    iso_req = [r for r in completed if r["phase"] == "isolated_failure" and r["status"] >= 400]
    iso_down = [
        t for t in transitions if iso["t_ns"] <= t["t_ns"] < phase_at["sustained_failure"]["t_ns"] and t["to"] == "DOWN"
    ]
    rows.append(
        {
            "run_id": run_id,
            "repetition": rep,
            "sqlite_enabled": bool(db_path),
            "event_kind": "isolated_failure",
            "phase": "isolated_failure",
            "expected_transition": "none (below threshold)",
            "server_change_utc": iso_ns(iso["t_ns"]),
            "server_change_rel_s": round(iso["rel_s"], 4),
            "value_reference": f"failed_checks={len(iso_req)};down_transitions=0",
            "value_observed": f"down_transitions={len(iso_down)}",
            "difference": len(iso_down),
        }
    )
    ref_fail = sum(
        1
        for r in completed
        if r["aborted"] or not (200 <= r["status"] < 400) or (r["done_ns"] - r["arrive_ns"]) / 1e6 >= 800
    )
    comparisons = [
        ("checks_total", len(completed), summary.get("checks")),
        ("failed_checks", ref_fail, summary.get("failed checks")),
        ("incidents", len(ref_incidents), summary.get("incidents")),
        ("uptime_pct_policy", ref_uptime, summary.get("uptime_pct")),
        ("inflight_at_shutdown_requests", len(inflight), None),
        ("final_state", ref_transitions[-1]["to"] if ref_transitions else None, summary.get("state")),
        ("exit_code", 0, exit_code),
    ]
    outage_phases = [("sustained_failure", "recovery"), ("timeout", "healthy_after_timeout")]
    for idx, (a, b) in enumerate(outage_phases):
        server_s = (phase_at[b]["t_ns"] - phase_at[a]["t_ns"]) / 1e9
        policy_s = (ref_incidents[idx][1] - ref_incidents[idx][0]) / 1e9 if idx < len(ref_incidents) else None
        comparisons.append((f"incident{idx + 1}_server_outage_s_vs_policy_s", server_s, policy_s))
    db_info = {}
    wall_of = {r["arrive_ns"]: r.get("arrive_wall_ns") for r in completed}
    if db_path:
        con = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
        db_checks = con.execute("SELECT count(*), sum(NOT ok) FROM checks").fetchone()
        db_inc = con.execute("SELECT start, end FROM incidents ORDER BY start").fetchall()
        con.close()
        comparisons.append(("db_checks_vs_summary", summary.get("checks"), db_checks[0]))
        comparisons.append(("db_failed_checks_vs_summary", summary.get("failed checks"), db_checks[1]))
        comparisons.append(("db_incidents_vs_policy", len(ref_incidents), len(db_inc)))
        for idx, (s, e) in enumerate(db_inc):
            if idx < len(ref_incidents):
                rs, re_ = ref_incidents[idx]
                comparisons.append(
                    (
                        f"db_incident{idx + 1}_duration_s_vs_policy",
                        (re_ - rs) / 1e9 if re_ else None,
                        (e - s) / 1e9 if e else None,
                    )
                )
                comparisons.append(
                    (
                        f"db_incident{idx + 1}_start_minus_first_failed_arrival_wall_ms",
                        0,
                        (s - wall_of[rs]) / 1e6 if rs in wall_of else None,
                    )
                )
        db_info = {"db_checks": db_checks[0], "db_incidents": len(db_inc)}
    for name, ref, obs in comparisons:
        diff = None
        if isinstance(ref, (int, float)) and isinstance(obs, (int, float)):
            diff = obs - ref
        rows.append(
            {
                "run_id": run_id,
                "repetition": rep,
                "sqlite_enabled": bool(db_path),
                "event_kind": "comparison",
                "phase": name,
                "value_reference": ref,
                "value_observed": obs,
                "difference": diff,
            }
        )
    events_out.write(rows)
    cpu = mon.usage.ru_utime + mon.usage.ru_stime
    wall = (mon.end_ns - mon.start_ns) / 1e9
    status = "ok" if exit_code == 0 and summary.get("checks") is not None else "failed"
    step_ms = (wall_offset() - WALL_OFFSET[0]) / 1e6
    clock_note = f"wall clock stepped {step_ms:+.1f} ms during run; " if abs(step_ms) > 20 else ""
    note = clock_note + (
        f"monitor interval={MONITOR_ARGS['interval']} timeout={MONITOR_ARGS['timeout']} "
        f"threshold={MONITOR_ARGS['fail_threshold']} degraded={MONITOR_ARGS['degraded']}; "
        f"checks={summary.get('checks')}; in-flight at SIGINT={len(inflight)}; {json.dumps(db_info)}"
    )
    row = {
        "run_id": run_id,
        "experiment_group": "F_monitor",
        "repetition": rep,
        "order_index": order,
        "timestamp_utc": iso_ns(mon.start_ns),
        "commit": ev.commit,
        "tool": "httpload-monitor",
        "tool_version": ev.versions["httpload"],
        "endpoint": "/health (scripted)",
        "concurrency": 1,
        "tui_enabled": False,
        "sqlite_enabled": bool(db_path),
        "configured_duration_s": SHUTDOWN_AFTER_S,
        "wall_duration_s": wall,
        "requests_attempted": len(requests),
        "responses_completed": len(completed),
        "generator_cpu_user_s": mon.usage.ru_utime,
        "generator_cpu_system_s": mon.usage.ru_stime,
        "generator_cpu_util_pct_1cpu": 100 * cpu / wall,
        "generator_cpu_util_pct_alloc": 100 * cpu / wall / GEN_CPU_COUNT,
        "generator_peak_rss_mib": mon.usage.ru_maxrss / 1024,
        "exit_code": exit_code,
        "status": status,
        "config_file": ev.rel(f"{prefix}.schedule.json"),
        "raw_log": ev.rel(mon.stdout_path),
        "notes": note,
        "db_rows": db_info.get("db_checks"),
    }
    ev.results.write([row])
    Path(f"{prefix}.argv.json").write_text(json.dumps({"monitor": mon.argv, "schedule": MONITOR_SCHEDULE}))
    return row


RESTART_SCHEDULE = [
    {"name": "healthy", "at_s": 0},
    {"name": "failing_1", "at_s": 5, "status": 503},
    {"name": "healthy_2", "at_s": 20},
    {"name": "failing_2", "at_s": 26, "status": 503},
    {"name": "healthy_3", "at_s": 38},
]


def restart_scenario(ev, events_out):
    run_id = "R000-monitor-restart-db"
    prefix = ev.raw / "runs" / run_id
    db_path = ev.raw / "runs" / f"{run_id}.db"
    server, log_path = start_monitor_server(ev, RESTART_SCHEDULE, prefix)
    start_ns = read_jsonl(log_path)[0]["t_ns"]

    def until(s):
        while time.monotonic_ns() < start_ns + s * 1e9:
            time.sleep(0.02)

    def db_snapshot(label):
        con = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
        inc = con.execute("SELECT rowid, start, end, cause FROM incidents ORDER BY rowid").fetchall()
        checks = con.execute("SELECT count(*) FROM checks").fetchone()[0]
        con.close()
        return {"label": label, "checks": checks, "incidents": inc}

    steps = []
    segments = [("A", 0, 12), ("B", 13, 34), ("C", 42, 48)]
    for name, a, b in segments:
        until(a)
        mon = MonitorProcess(ev, f"{prefix}.{name}", db_path, None)
        until(b)
        code = mon.stop()
        _, transitions, summary = parse_monitor_lines(mon.lines)
        snap = db_snapshot(f"after_{name}")
        steps.append(
            {
                "segment": name,
                "start_s": a,
                "stop_s": b,
                "exit_code": code,
                "in_memory_summary": summary,
                "transitions": [f"{t['from']}→{t['to']}" for t in transitions],
                "db": snap,
            }
        )
    server.send_signal(signal.SIGTERM)
    server.wait(timeout=10)
    Path(f"{prefix}.steps.json").write_text(json.dumps(steps, indent=2, default=str))
    rows = []
    for st in steps:
        open_rows = [r for r in st["db"]["incidents"] if r[2] is None]
        closed = [r for r in st["db"]["incidents"] if r[2] is not None]
        rows.append(
            {
                "run_id": run_id,
                "repetition": 1,
                "sqlite_enabled": True,
                "event_kind": "restart_persistence",
                "phase": f"after_segment_{st['segment']}",
                "value_reference": f"in_memory_checks={st['in_memory_summary'].get('checks')};"
                f"in_memory_incidents={st['in_memory_summary'].get('incidents')};"
                f"final_state={st['in_memory_summary'].get('state')}",
                "value_observed": f"db_checks={st['db']['checks']};db_incidents={len(st['db']['incidents'])};"
                f"db_open={len(open_rows)};db_closed={len(closed)}",
                "notes": "transitions=" + ",".join(st["transitions"]),
            }
        )
    events_out.write(rows)
    print(json.dumps(steps, indent=1, default=str))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=["http", "monitor"])
    parser.add_argument("--run-dir", required=True)
    parser.add_argument("--groups", default="A_patterns")
    parser.add_argument("--reps", type=int, default=5)
    parser.add_argument("--seed", type=int, default=20261101)
    parser.add_argument("--warmup", type=int, default=5)
    parser.add_argument("--measure", type=int, default=20)
    parser.add_argument("--pilot", action="store_true")
    parser.add_argument("--skip-restart", action="store_true")
    args = parser.parse_args()
    if args.command == "http":
        cmd_http(args)
    else:
        cmd_monitor(args)


if __name__ == "__main__":
    sys.exit(main())
