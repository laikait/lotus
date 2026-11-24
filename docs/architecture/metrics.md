# Metrics

Status: a Prometheus-format endpoint on its own listener. It covers requests,
latency, connections, reverse-proxy backends, PHP processes, reloads and
certificate expiry. Not built yet: per-route or per-listener series, request
size histograms, TLS handshake and QUIC connection counters, OpenTelemetry
export, and a push mode.

## Turning it on

```
server {
    metrics {
        address "127.0.0.1:9180"
        # path "/metrics"                 # the default
        # bearer_token env("METRICS_TOKEN")
        # allow [ "10.0.0.0/8" ]
    }
}
```

See `configs/examples/metrics.lotus`. Without a `metrics` block nothing is
served. Prometheus then scrapes `http://127.0.0.1:9180/metrics`:

```yaml
scrape_configs:
  - job_name: lotus
    static_configs: [{ targets: ["127.0.0.1:9180"] }]
    # with a token:
    # authorization: { credentials_file: /etc/prometheus/lotus.token }
```

| Directive | Meaning |
|---|---|
| `address` | where to listen; required |
| `path` | the URL path that serves the metrics; any other path is 404. Default `/metrics` |
| `bearer_token` | if set, requests need `Authorization: Bearer TOKEN` (constant-time comparison, redacted in all output) |
| `allow` | if set, only these networks may read; others get 403. It tests the connection's own address, never `X-Forwarded-For`, so a client can't claim to be inside |

## Who can read it

Metrics tell an outsider which sites exist, how busy they are and which
backends are failing. The listener is separate from the sites' listeners (a
site never serves `/metrics`, and the metrics port serves nothing else), and it
is set up by the administrator only: a tenant file that tries to open one is an
error.

A listener that isn't on a loopback address (127.0.0.0/8, `::1`, `localhost`) is
refused in the configuration unless it has a `bearer_token` or an `allow` list.
The token is sent in clear over plain HTTP: use it on a trusted network, or put
the port behind a TLS-terminating proxy. At most 4 scrapes run at once; more get
503.

## What there is

Labels are only names that the administrator wrote (site names, upstream names,
backend addresses). Nothing a client sends (path, query, Host, User-Agent,
address) is ever a label, so a client can't make the number of series grow.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `lotus_http_requests_total` | counter | `site`, `protocol` (`HTTP/1`, `HTTP/2`, `HTTP/3`), `code_class` (`1xx` ... `5xx`, `other`) | responses sent. `site=""` is requests no site answered |
| `lotus_http_response_bytes_total` | counter | `site` | body bytes sent |
| `lotus_http_request_duration_seconds` | histogram | `site` | time from the start of the request to the end of the response; buckets from 5 ms to 60 s |
| `lotus_http_rate_limited_total` | counter | `site` | 429 answers from a rate limit or a password lock-out |
| `lotus_requests_dropped_total` | counter | | requests rejected by the HTTP server before routing (malformed, too large, too slow) |
| `lotus_connections_active` / `_limit` | gauge | | open connections (TCP and QUIC) against `max_connections` |
| `lotus_connections_rejected_total` | counter | `limit` | connections closed at once: `max_connections` or `max_connections_per_ip` |
| `lotus_upstream_backend_healthy` | gauge | `upstream`, `backend` | 1 if the backend takes requests |
| `lotus_upstream_backend_active_requests` | gauge | `upstream`, `backend` | requests in progress |
| `lotus_upstream_backend_requests_total`, `_failures_total` | counter | `upstream`, `backend` | requests sent (retries included) and those that failed |
| `lotus_php_process_restarts_total` | counter | `backend` | managed PHP processes replaced |
| `lotus_certificate_ready` | gauge | `name`, `source` (`file`, `acme`), `site` (files) | 1 if a certificate is loaded |
| `lotus_certificate_expiry_timestamp_seconds` | gauge | same | when it expires |
| `lotus_config_generation` | gauge | | the configuration number (1 at start, +1 per reload that changed it) |
| `lotus_config_reloads_total` | counter | `result` (`success`, `failure`) | reloads |
| `lotus_config_last_reload_success_timestamp_seconds` | gauge | | last good load |
| `lotus_build_info` | gauge | `version`, `go_version` | always 1 |
| `lotus_start_time_seconds`, `lotus_uptime_seconds` | gauge | | |
| `go_goroutines`, `go_memstats_*`, `go_gc_cycles_total` | | | the Go runtime |

Counters are kept per site and survive reloads. A site that is removed from the
configuration stops being reported; one that is added again resumes counting
where it was.

## Alerts worth having

```yaml
- alert: LotusCertificateExpiring
  expr: min(lotus_certificate_expiry_timestamp_seconds) - time() < 14 * 86400
- alert: LotusCertificateMissing
  expr: lotus_certificate_ready == 0
  for: 30m
- alert: LotusBackendDown
  expr: lotus_upstream_backend_healthy == 0
  for: 2m
- alert: LotusServerErrors
  expr: sum by (site) (rate(lotus_http_requests_total{code_class="5xx"}[5m])) / sum by (site) (rate(lotus_http_requests_total[5m])) > 0.05
- alert: LotusRejectingConnections
  expr: rate(lotus_connections_rejected_total[5m]) > 0
- alert: LotusReloadFailing
  expr: increase(lotus_config_reloads_total{result="failure"}[10m]) > 0
```

p95 latency per site:
`histogram_quantile(0.95, sum by (site, le) (rate(lotus_http_request_duration_seconds_bucket[5m])))`.

## Notes

- A request is counted when it has finished (after the response was written),
  so a request in progress is not in the totals. A scrape that runs at the same
  moment as a burst may see a few requests of it and not the rest.
- Reading the Go memory statistics stops the program for a very short time on
  each scrape; scrape every 15 seconds or more, not every second.
- The `metrics` block can't be changed by `lotus reload`; that needs a restart.
- Counters start at zero when LOTUS starts; Prometheus's `rate()` handles that.
