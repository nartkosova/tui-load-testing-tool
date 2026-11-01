import datetime
import json
import os
import platform
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import harness

REPO = harness.REPO


def sh(*argv, cwd=REPO):
    try:
        out = subprocess.run(argv, capture_output=True, text=True, timeout=60, cwd=cwd)
        return (out.stdout + out.stderr).strip()
    except (OSError, subprocess.TimeoutExpired) as exc:
        return f"unavailable: {exc}"


def read(path):
    try:
        return Path(path).read_text().strip()
    except OSError as exc:
        return f"unavailable: {exc}"


def main():
    run_dir = Path(sys.argv[1]).resolve()
    meminfo = dict(line.split(":", 1) for line in read("/proc/meminfo").splitlines())
    lscpu = sh("lscpu")
    model = next((line.split(":", 1)[1].strip() for line in lscpu.splitlines() if line.startswith("Model name")), None)
    gomaxprocs_probe = run_dir / "raw" / "gomaxprocs.go"
    gomaxprocs_probe.write_text(
        'package main\n\nimport (\n\t"fmt"\n\t"runtime"\n)\n\nfunc main() {\n\tfmt.Println(runtime.GOMAXPROCS(0))\n}\n'
    )
    default_procs = sh("go", "run", str(gomaxprocs_probe))
    pinned_procs = sh("taskset", "-c", harness.GEN_CPUS, "go", "run", str(gomaxprocs_probe))
    env = {
        "created_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
        "repository": {
            "path": str(REPO),
            "commit": sh("git", "rev-parse", "HEAD"),
            "commit_time": sh("git", "show", "-s", "--format=%cI", "HEAD"),
            "branch": sh("git", "rev-parse", "--abbrev-ref", "HEAD"),
            "tracked_changes_vs_head": sh("git", "diff", "--stat", "HEAD"),
            "status_porcelain": sh("git", "status", "--porcelain"),
            "dirty_note": "Only the untracked eval/ directory (evaluation code and results) differs from HEAD; "
            "no tracked file is modified. Go build info therefore reports vcs.modified=true.",
            "agents_md": "AGENTS.md not present in the checkout; the user's stated rules (no code comments, "
            "formatting/linting) were applied (gofmt, go vet, ruff check, ruff format).",
        },
        "build": {
            "httpload": f"go build -o {run_dir}/bin/httpload ./cmd/httpload (as make build; default flags, CGO_ENABLED=1, GOAMD64=v1, no -race, no -cover)",
            "benchserver": f"CGO_ENABLED=0 go build -o {run_dir}/bin/benchserver ./bench/server",
            "monitorserver": f"go build -o {run_dir}/bin/monitorserver ./eval/monitorserver",
            "latencyacc": f"go build -o {run_dir}/bin/latencyacc ./eval/latency",
            "target_image": f"docker build -t httpload-eval-target:e402cc8 -f eval/target/Dockerfile {run_dir}/bin",
            "httpload_buildinfo": "raw/httpload_buildinfo.txt",
            "sha256": sh("sha256sum", *[str(p) for p in sorted((run_dir / "bin").iterdir())]),
        },
        "system": {
            "os": read("/etc/os-release").splitlines()[0],
            "kernel": platform.release(),
            "uname": " ".join(platform.uname()),
            "cpu_model": model,
            "logical_cpus": os.cpu_count(),
            "lscpu": lscpu,
            "cpu_topology": sh("lscpu", "-e=CPU,CORE,MAXMHZ"),
            "core_types": "CPUs 0-3: 2 P-cores with SMT (max 4.4 GHz); CPUs 4-11: 8 E-cores (max 3.3 GHz)",
            "mem_total_mib": int(meminfo["MemTotal"].split()[0]) // 1024,
            "mem_available_mib_at_start": int(meminfo["MemAvailable"].split()[0]) // 1024,
            "swap_total_mib": int(meminfo["SwapTotal"].split()[0]) // 1024,
            "swap_free_mib_at_start": int(meminfo["SwapFree"].split()[0]) // 1024,
            "cpufreq_governor": read("/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor"),
            "platform_profile": read("/sys/firmware/acpi/platform_profile"),
            "intel_no_turbo": read("/sys/devices/system/cpu/intel_pstate/no_turbo"),
            "ac_online": read("/sys/class/power_supply/ADP1/online"),
            "battery_status": read("/sys/class/power_supply/BAT0/status"),
            "ulimit_nofile": sh("bash", "-c", "ulimit -n"),
            "ip_local_port_range": read("/proc/sys/net/ipv4/ip_local_port_range"),
            "tcp_tw_reuse": read("/proc/sys/net/ipv4/tcp_tw_reuse"),
            "tcp_fin_timeout": read("/proc/sys/net/ipv4/tcp_fin_timeout"),
            "somaxconn": read("/proc/sys/net/core/somaxconn"),
            "loadavg_at_start": read("/proc/loadavg"),
            "uptime": read("/proc/uptime"),
            "background_containers": sh("docker", "ps", "--format", "{{.Names}} {{.Image}} {{.Status}}"),
            "background_container_stats": sh(
                "docker", "stats", "--no-stream", "--format", "{{.Name}} cpu={{.CPUPerc}} mem={{.MemUsage}}"
            ),
        },
        "tool_versions": {
            "httpload": f"e402cc8 ({sh('go', 'version')})",
            "k6": sh(str(run_dir / "tools" / "k6"), "version"),
            "wrk": sh("bash", "-c", "wrk -v 2>&1 | head -1"),
            "hey": "0.1.4 (Debian " + sh("dpkg-query", "-W", "-f=${Version}", "hey") + ")",
        },
        "other_versions": {
            "go": sh("go", "version"),
            "gomaxprocs_default": default_procs,
            "gomaxprocs_under_taskset_generator_cpus": pinned_procs,
            "sqlite_driver": "modernc.org/sqlite v1.38.2 (pure Go, modernc.org/libc v1.66.3)",
            "hdrhistogram": "github.com/HdrHistogram/hdrhistogram-go v1.4.0",
            "bubbletea": "github.com/charmbracelet/bubbletea v1.3.10",
            "k6_source": "official release tarball k6-v2.3.0-linux-amd64.tar.gz, sha256 verified against k6-v2.3.0-checksums.txt",
            "k6_binary_sha256": sh("sha256sum", str(run_dir / "tools" / "k6")),
            "wrk_package": sh("dpkg-query", "-W", "-f=${Package} ${Version}", "wrk"),
            "docker": sh("docker", "version", "--format", "client={{.Client.Version}} server={{.Server.Version}}"),
            "python": sys.version,
            "taskset": sh("bash", "-c", "taskset --version"),
            "ruff": sh("ruff", "--version"),
        },
        "resource_measurement": {
            "generator_cpu_and_peak_rss": "os.wait4() rusage of the generator process (ru_utime, ru_stime, ru_maxrss); "
            "covers the whole process lifetime",
            "generator_and_target_samples": "/proc/<pid>/stat utime+stime and /proc/<pid>/status VmRSS sampled every 0.25 s "
            "(clock tick resolution 10 ms)",
            "target_cpu": "difference of /proc/<pid>/stat utime/stime of the target process between immediately before "
            "the measured generator starts and immediately after it exits",
            "target_peak_rss": "VmHWM of the target process after resetting it via /proc/<pid>/clear_refs (5) "
            "after warm-up, so it covers only the measured run",
            "cpu_utilisation_normalisation": "*_pct_1cpu = cpu_seconds / wall_seconds * 100 (100 = one logical CPU busy); "
            "*_pct_alloc = *_pct_1cpu / 4 (share of the 4 CPUs the process was pinned to)",
            "tcp_counters": "/proc/net/snmp Tcp ActiveOpens/AttemptFails deltas (system-wide) and TIME_WAIT sockets from /proc/net/tcp{,6}",
            "unavailable": "pidstat, perf, /usr/bin/time not used (perf and GNU time not installed); "
            "cgroup memory.peak of the container not readable without root",
        },
        "target": {
            "image": "httpload-eval-target:e402cc8",
            "image_id": sh("docker", "image", "inspect", "-f", "{{.Id}}", "httpload-eval-target:e402cc8"),
            "contents": "FROM scratch with the statically built bench/server binary, USER 1000:1000",
            "run": f"docker run -d --rm --network host --cpuset-cpus {harness.TARGET_CPUS} --ulimit nofile=524288:524288",
            "endpoints": "/fast writes 'ok' (2 bytes) immediately; /slow sleeps 10 ms then writes 'ok'",
            "listen": "127.0.0.1:8099 (host network namespace, loopback; no Docker NAT/proxy)",
            "memory_limit": "none",
            "fresh_container_per_run": True,
            "http": "Go net/http server defaults, HTTP/1.1, keep-alive enabled",
        },
        "arrangement": {
            "co_located": True,
            "description": "Client and server share one laptop. Generators pinned to CPUs "
            f"{harness.GEN_CPUS} (4 E-cores) with taskset; target pinned to CPUs {harness.TARGET_CPUS} "
            "(4 E-cores) with --cpuset-cpus; harness, OS and background processes run unpinned and mostly on "
            "the P-cores 0-3. Traffic uses the loopback interface.",
        },
        "protocol": {
            "warmup_s": 5,
            "measured_s": 20,
            "repetitions": 5,
            "cancellation_repetitions": 3,
            "seed": 20261101,
            "order": "per experiment group, all configuration x repetition items shuffled with "
            "random.Random(f'{seed}-{group}'); groups run sequentially A, B, C, D, E, F",
            "cooldown": "min 3 s; extended (max 30 s) while whole-system CPU busy > baseline + 10 points, "
            "TIME_WAIT is not drained: each warm-up recreates the pool before its measured run; time_wait_before_measured records it",
            "request_timeout_s": harness.REQUEST_TIMEOUT_S,
            "http_method": "GET",
            "http_version": "HTTP/1.1 (plaintext), keep-alive enabled by every tool",
            "pty_size": f"{harness.TUI_ROWS}x{harness.TUI_COLS}, TERM=xterm-256color",
        },
        "baseline": {"busy_pct_10s": round(harness.cpu_busy_pct(10.0), 2), "time_wait": harness.time_wait_count()},
    }
    (run_dir / "environment.json").write_text(json.dumps(env, indent=2))
    print(json.dumps({k: env[k] for k in ("baseline", "tool_versions")}, indent=1))
    print(env["other_versions"]["gomaxprocs_default"], env["other_versions"]["gomaxprocs_under_taskset_generator_cpus"])


if __name__ == "__main__":
    main()
