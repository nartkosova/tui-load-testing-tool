done = function(summary, latency, requests)
  local e = summary.errors
  io.write(string.format(
    'WRKJSON {"duration_us":%d,"requests":%d,"bytes":%d,"connect":%d,"read":%d,"write":%d,"status":%d,"timeout":%d,"min_us":%d,"mean_us":%.3f,"p50_us":%d,"p95_us":%d,"p99_us":%d,"max_us":%d}\n',
    summary.duration, summary.requests, summary.bytes, e.connect, e.read, e.write, e.status, e.timeout,
    latency.min, latency.mean, latency:percentile(50), latency:percentile(95), latency:percentile(99), latency.max))
end
