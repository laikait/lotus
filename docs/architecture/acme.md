# Automatic certificates (ACME)

Status: HTTP-01, TLS-ALPN-01 and DNS-01 (so wildcard certificates) work against
any ACME CA (Let's Encrypt by default), tested against Pebble, Let's Encrypt's
test CA. DNS-01 has two providers: Cloudflare, and `exec`, which runs a program
of yours so that any DNS service can be used. Not built yet: other built-in DNS
providers, on-demand issuance (deliberately), OCSP stapling, a certificate status
command, and certificate storage shared between several LOTUS instances beyond
what the storage directory gives.

## Turning it on

A site on an `https` listener uses automatic TLS by default. All that is needed
is an `acme` block:

```
server {
    listen "http"  { address "0.0.0.0:80" }
    listen "https" { address "0.0.0.0:443"
                     protocol https }

    tls {
        acme {
            email "admin@example.com"
        }
    }

    site "example.com" {
        root "/var/www/example"
        aliases [ "www.example.com" ]
    }
}
```

At start LOTUS gets a certificate for `example.com` and one for
`www.example.com` (each name has its own certificate), keeps using them across
restarts, and renews each about 30 days before it expires. See `configs/examples/acme.conf`.

| Directive | Meaning |
|---|---|
| `email` | contact address registered with the CA |
| `ca` | ACME directory URL; default is Let's Encrypt production. Use the staging URL (`https://acme-staging-v02.api.letsencrypt.org/directory`) while testing |
| `storage` | where the account key, certificates and private keys are kept; default `/var/lib/lotus/certs` (`%ProgramFiles%\Lotus\certs` on Windows). Created readable by the LOTUS user only |
| `challenges` | `[ "http-01", "tls-alpn-01" ]` by default, plus `"dns-01"` when there is a `dns` block; use one when the other ports are closed |
| `dns` | how the record of a DNS-01 validation is published; see below |
| `http_port`, `tls_alpn_port` | the ports the CA connects to, if not 80 and 443 (a port forward) |
| `ca_root` | root certificate of a private or test CA |
| `eab_key_id`, `eab_hmac_key` | external account binding for CAs that need it |

## What requests it

- Which names: the name and `aliases` of each site that uses `tls automatic`. No
  other name is ever requested.
- **A client can't cause an order.** Certificates are never obtained during a TLS
  handshake, and a handshake for a name that is not a site is refused. (No
  on-demand issuance: it would let anyone make the server ask the CA for
  arbitrary names and burn its rate limit.)
- Names a public CA can't issue for (an IP address, `localhost`, a name without a
  dot, `.local`) are skipped with a warning in the log; the site then has no
  certificate. For those, use `tls manual`.
- Wildcard names (`*.example.com`) need DNS-01: without a `dns` block they are
  refused in the configuration (use `tls manual` with a wildcard certificate, or
  add the block). A wildcard certificate covers one label (`a.example.com`, not
  `a.b.example.com`) and not the bare name: list `example.com` too, and it gets
  its own certificate.

## How the CA's checks are answered

LOTUS answers the checks with its own listeners, so no extra port is bound.

- **HTTP-01**: the CA fetches `http://name/.well-known/acme-challenge/token` on
  port 80. Any plain-HTTP listener answers that path before anything else (no
  site, rate limit or `.htaccess` is involved), then serves everything else
  normally. The listener on port 80 must exist.
- **TLS-ALPN-01**: the CA opens a TLS connection to port 443 offering the
  protocol `acme-tls/1`; the https listener answers with the validation
  certificate. The listener on port 443 must exist. This works when port 80 is
  closed.

- **DNS-01**: LOTUS publishes a TXT record `_acme-challenge.name` through the
  provider in the `dns` block, waits until it can see it, lets the CA check, then
  removes it. No port needs to be open, which is what makes it work for hosts
  behind a firewall and for wildcards. See the next section.

If the CA reaches none of the checks it is offered, issuance fails and is retried with growing
delays; the log says which name failed and why.

## Seeing the certificates

```
$ lotus certs
NAME         SOURCE                  STATUS    EXPIRES     LEFT  ISSUER             COVERS
example.com  acme (renewed by lotus) ok        2026-09-14  81d   Let's Encrypt R10  -
www.example.com acme (renewed by lotus) pending -         -     -                  -
shop.test    file                    expiring  2026-06-06  5d    Example CA         shop.test, www.shop.test
```

It asks the running server, so it shows what is loaded now, for `tls manual`
certificates (from files) and for the names obtained from a CA. `pending` is a
name whose certificate has not been obtained yet (the first issuance, or a CA
that refuses); the log says why. Options: `--json`, `--warn-days N` (default 14),
`--control SOCKET`.

The exit code is **8** when any certificate is pending, expired or ends within the
warning window, so it works as a monitoring check:

```sh
lotus certs --warn-days 21 || echo "certificate problem"
```

A certificate obtained by ACME is renewed about 30 days before it ends, so one
that shows `expiring` (under 14 days) means renewal has been failing for weeks.
The metrics `lotus_certificate_expiry_timestamp_seconds` and
`lotus_certificate_ready` carry the same facts for alerting.

## DNS-01 and wildcard certificates

```
tls {
    acme {
        email "admin@example.com"
        dns {
            provider "cloudflare"
            api_token env("CLOUDFLARE_API_TOKEN")
        }
    }
}
site "example.com" { aliases [ "*.example.com" ] ... }
```

See `configs/examples/acme-wildcard.conf`. With a `dns` block LOTUS also offers
DNS-01 for ordinary names; the CA picks. To use DNS only, add
`challenges [ "dns-01" ]`.

| Directive in `dns` | Meaning |
|---|---|
| `provider` | `cloudflare` or `exec` |
| `api_token` | Cloudflare API token with Zone.DNS:Edit (and Zone:Read unless limited to one zone). Not a global key. Redacted in all output; use `env(...)` |
| `command` | the program of the `exec` provider |
| `propagation_delay` | wait this long after publishing before checking |
| `propagation_timeout` | how long to keep checking that the record is visible (default 2m) |
| `propagation_check` | `false` asks the CA without checking first; for split-horizon DNS where the servers you can query don't show what the CA sees |
| `resolvers` | DNS servers (`host:port`) used for the checks and to find which zone a name belongs to |

### The `exec` provider

For any DNS service that has a command-line tool or API you can script. LOTUS
runs the program (no shell) with one argument, `present` or `cleanup`, and the
record in the environment:

| Variable | Example |
|---|---|
| `LOTUS_ACME_ZONE` | `example.com` |
| `LOTUS_ACME_NAME` | `_acme-challenge.example.com` |
| `LOTUS_ACME_VALUE` | the TXT value |
| `LOTUS_ACME_TTL` | `60` |

Exit status 0 means done; anything else is a failure and the first 2 KiB of its
output goes to the log. It has one minute per call. It runs as the LOTUS user, so
make it writable by administrators only, and give the credentials it needs the
least access possible. Only the administrator configuration can name it; a
tenant file can't.

```sh
#!/bin/sh
# present: create the TXT record; cleanup: delete it
case "$1" in
  present) my-dns-tool add    TXT "$LOTUS_ACME_NAME" "$LOTUS_ACME_VALUE" ;;
  cleanup) my-dns-tool remove TXT "$LOTUS_ACME_NAME" "$LOTUS_ACME_VALUE" ;;
esac
```

### Safety of the DNS credentials

- Treat the token like the account key: whoever has it can change your DNS and
  so get certificates for your domains. Scope it to one zone.
- Prefer `env("NAME")` to writing it into the file; `lotus config` output and the
  logs never show it.
- A certificate is requested only for names of configured sites, whatever the
  provider can do.

## What happens at start and on reload

- At **start**, certificates already in `storage` are loaded as soon as the
  listeners are up, so a restart doesn't order anything new and HTTPS works at once. Missing or expiring ones are obtained in the background (the server is up
  and serves plain HTTP and other sites meanwhile; a handshake for a name whose
  certificate isn't there yet fails until it arrives).
- A **reload** that adds a site with automatic TLS starts obtaining its
  certificate. Removing a site doesn't stop its certificate being renewed until
  the next restart (the certificate is no longer served).
- A reload can't change `acme` itself (CA, account, storage, ports): that gives a
  "restart required" error and keeps the running configuration.

## Storage, keys and secrets

`storage` holds the ACME account key and the private key of every certificate. It
is created with mode 0700. Treat it like the private keys it contains: don't put it
in a web root, back it up, and don't share it between machines that aren't meant
to be the same server. Lost certificates are issued again, but CAs limit how many
per week (Let's Encrypt: 50 per registered domain), so back it up.

Several LOTUS processes can share a storage directory (on a shared file system):
certificate files are locked while being written, and one process answers
challenges started by another.

## Testing against Pebble

```
go install github.com/letsencrypt/pebble/v2/cmd/pebble@v2.10.3
go install github.com/letsencrypt/pebble/v2/cmd/pebble-challtestsrv@v2.10.3
export LOTUS_PEBBLE_DIR=$(go env GOPATH)/bin
export LOTUS_PEBBLE_TESTDIR=<module cache>/github.com/letsencrypt/pebble/v2@v2.10.3/test
go test ./internal/server -run AutomaticCertificates
```

The tests start Pebble and its mock DNS themselves, issue certificates over the
three challenges (DNS-01 through the `exec` provider, including a wildcard), verify the chain and the name, make real HTTPS requests, check that a
restart reuses the stored certificate, and that a reload adds a site. Without the
variables they are skipped.

## Limits

- The first issuance happens after the listeners are up; there is no option to
  hold start until it completes.
- Renewal failures are logged. `lotus certs` lists every certificate with its
  expiry (see below), and the metrics endpoint exports expiry for alerting. There
  is no command to force a renewal; renewal is automatic.
- Let's Encrypt has stopped OCSP; LOTUS doesn't staple.
- One ACME account and CA per server, and one DNS provider.
- The zone of a name is found by asking DNS for its SOA record. A provider that
  manages a zone the public DNS doesn't show (an internal zone) won't work; there
  is no setting to name the zone yet.
