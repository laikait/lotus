# HTTP/3

Status: works over UDP with QUIC (RFC 9000) and HTTP/3 (RFC 9114), using the
`quic-go` library, tested with its client. Not built yet: 0-RTT (refused on
purpose), WebTransport and HTTP datagrams, PROXY protocol for QUIC, per-listener
tuning of QUIC itself, and a metrics endpoint for QUIC connections.

## Turning it on

```
listen "https" {
    address "0.0.0.0:443"
    protocol https
    http3 true
}
```

See `configs/examples/http3.lotus`. The listener serves HTTP/1.1 and HTTP/2 on
TCP port 443 as before, and HTTP/3 on **UDP port 443**. That UDP port must be
open in the firewall and in any cloud security group; this is the usual reason
HTTP/3 "doesn't work".

Clients learn about HTTP/3 from an `Alt-Svc` header on responses over TCP:

```
Alt-Svc: h3=":443"; ma=86400
```

They try HTTP/3 on their next request and fall back to TCP if UDP is blocked, so
turning it on can't make a site unreachable. The header is sent only on HTTPS
responses, never on HTTP/3 ones, and a proxied backend's own `Alt-Svc` is removed.

## What is the same

HTTP/3 requests go through the same handler as the other protocols. Sites,
virtual hosts, routes, `.htaccess`, PHP, the reverse proxy, rate limits, security
headers, error pages and access logs behave identically (the access log shows
`HTTP/3.0`). Certificates are the same too: manual ones, and automatic ones from
ACME (a client that connects by HTTP/3 before the certificate has been obtained
fails the handshake just as over TCP).

- The `Host` header must select the same site as the name the client gave in the
  handshake, or the answer is 421 (the check that stops one site's connection
  being used to reach another).
- `max_connections` and `max_connections_per_ip` count QUIC connections together
  with TCP ones. A connection over the limit is closed at once with the HTTP/3
  error "excessive load". (A QUIC connection can carry many requests; one client
  address opening many connections is what the limit stops.)
- `max_header_bytes`, `header_timeout`, `idle_timeout`, `read_timeout` and
  `write_timeout` apply. The last two are per request, as with HTTP/2.
- At most 100 requests at a time on one connection.

## What is different

- **TLS 1.3 only.** QUIC requires it. `min_version` doesn't matter for HTTP/3.
- **No 0-RTT.** QUIC lets a returning client send a request in its first packet,
  but an attacker can replay such a request, and LOTUS can't know whether a
  request is safe to repeat. It is refused.
- **Source addresses are not verified** before the handshake except as the QUIC
  specification requires (the server never sends more than three times what it
  received). Under a flood of spoofed packets a server behind no filter spends CPU
  on handshakes; put UDP rate limits at the firewall if that is a risk for you.
- **`reload`** applies everything that applies to TCP listeners. Turning `http3`
  on or off, or changing the address, needs a restart, like any listener change.
- **Shutdown** sends the clients a GOAWAY, lets running requests finish until the
  shutdown time limit, and then closes the connections.

## Operating system settings

- **Linux:** QUIC wants large UDP socket buffers. If the log says "failed to sufficiently
  increase receive buffer size", raise `net.core.rmem_max` and `net.core.wmem_max`
  to 7500000 or more. HTTP/3 works without, but loses speed on fast links.
- **Firewalls:** allow UDP on the port, both ways.
- **Windows:** allow UDP in Windows Defender Firewall for the port.
- **Load balancers:** most must be told to pass UDP; a TCP-only load balancer in
  front means HTTP/3 never reaches LOTUS (and clients fall back to TCP).

## Checking it

```
curl --http3 -v https://example.com/        # curl built with HTTP/3
```

or a browser's network tools (the "Protocol" column shows `h3`). Browsers use
HTTP/3 only from the second request, after seeing `Alt-Svc`.

## Limits

- QUIC and HTTP/3 are implemented by the `quic-go` library. Its security fixes
  matter as much as Go's own: keep LOTUS built with a current release.
- Performance of HTTP/3 in Go is lower than HTTP/2 over TCP on the same hardware
  (UDP handling in user space). It is for latency on bad networks, not for
  throughput.
