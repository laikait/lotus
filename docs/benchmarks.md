# Benchmarks: method and limits

**Status:** the harness exists (`tools/bench`). Published results and the CI regression gate do not yet; see "What is not here".

```text
make bench                                    # builds bin/lotus, runs every scenario for 5 s each
go run ./tools/bench -lotus bin/lotus -only ping,proxy -duration 10s -connections 128
go run ./tools/bench -lotus bin/lotus -json mine.json                      # keep the result
go run ./tools/bench -lotus bin/lotus -baseline mine.json                  # later: fail on a regression
```

## What it measures

The tool starts the `lotus` program you give it (so you measure the binary you would ship, not a test build), a stub backend that answers 1 KiB, and writes the files below. For each scenario, N connections each send one request after the other (a *closed loop*: a new request goes out when the last answer has been read completely) for a warm-up of 1 s that is not counted, then for the measured time.

| Scenario | Request | What it shows |
|---|---|---|
| `ping` | `GET /ping`, a route that returns `ok` | the cost of the server itself: accept, parse, route, write |
| `static-1k` | a 1 KiB file | the static path: path resolution, open, headers, send |
| `static-100k` | a 100 KiB file that does not compress | throughput of large files |
| `static-gzip` | a 100 KiB text file with `Accept-Encoding: gzip` | on-the-fly compression |
| `proxy` | a proxied 1 KiB answer | the proxy path, with a keep-alive connection to the backend |
| `proxy-cached` | the same answer, served from the response cache | the cache hit path |
| `not-found` | a 404 | the error path |

Per scenario it reports requests per second, latency percentiles (p50, p90, p99, max) measured from sending the request to reading the last byte of the body, and megabytes per second of body. A scenario with any failed request (connection errors, 5xx, an unexpected 4xx) is marked and the run exits with 2: its numbers say nothing.

`-json` writes the results with the machine description (OS, architecture, CPU count, Go version, LOTUS version). `-baseline file` compares a run with such a file and exits with 3 if a scenario got more than `-max-rps-drop` percent (default 25) fewer requests per second or more than `-max-p99-rise` percent (default 50) higher p99. Runs from different machines are not compared at all.

## How to read the numbers

- **The load generator runs on the same machine.** It uses CPU that the server could use, and Go's HTTP client is not the fastest there is. Numbers are for comparing one LOTUS build with another on one machine, not for comparing LOTUS with other servers, and not absolute capacity. A fair comparison with Nginx or Caddy means running them with equivalent configuration, on a separate load machine, with a tool such as `wrk2` or `oha`: that is a separate, later exercise and the methodology for it will be written then.
- **Loopback, one connection pool, no TLS, HTTP/1.1.** TLS, HTTP/2 and HTTP/3 are not measured yet.
- **A closed loop hides queueing.** When the server stalls, the clients stall too, so the reported p99 is lower than what users with independent arrivals would see (the "coordinated omission" problem). Treat latencies as relative.
- **Short runs are noisy.** Use 10 s or more and run more than once; differences below roughly 10% are noise on a desktop. Close other programs. On laptops, plug in and disable power saving.
- **Timer resolution:** on Windows the clock has a coarse tick, so very small latencies read as 0.

## First observation (not a published result)

On the development machine, one run of 2–3 seconds per scenario showed these requests per second (32 connections):

| Scenario | Windows 11 | Linux (WSL2 Ubuntu, same machine) |
|---|---|---|
| ping | 25,900 | 17,100 |
| static-1k | 4,000 | 15,300 |
| proxy | 9,000 | 7,300 |
| proxy-cached | 19,900 | 23,100 |
| not-found | 15,100 | 24,200 |

The one number worth acting on is `static-1k` on Windows, about a fifth of `ping` and a quarter of Linux: a small static file is slower than a proxied request, which should not be. The static path resolves each URL with a check of every path component (the safety guarantee against traversal, symlink escapes and device names), and on Windows those file-system calls are slow, probably more so with antivirus scanning. A profile of that path is the next performance task. Everything else is plausible for a first measurement.

These runs were short and on a shared machine; they are a starting point, not a result to quote.

## What is not here

- A published results page for a fixed reference machine (needs a dedicated machine and a procedure for it).
- A CI gate. CI runners are shared and noisy; a gate with the 25% and 50% limits would fail at random. It needs either a self-hosted runner or a longer, repeated measurement with a statistical test. Until then `-baseline` is a local tool.
- Scenarios for TLS, HTTP/2, HTTP/3, PHP, WebSocket and many connections at once (10,000 idle connections: memory per connection).
- Memory and GC measurements (`GOMEMLIMIT`, cache size against pause times), which the spec asks for in §13.2.
