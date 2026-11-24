# Automatic certificates (ACME)

Status: HTTP-01 and TLS-ALPN-01 work against any ACME CA (Let's Encrypt by
default), tested against Pebble, Let's Encrypt's test CA. Not built yet: DNS-01
(and so wildcard certificates), on-demand issuance (deliberately), OCSP stapling,
a certificate status command, and certificate storage shared between several LOTUS
instances beyond what the storage directory gives.

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

At start LOTUS gets a certificate for `example.com` and `www.example.com`
(one certificate for both), keeps using it across restarts, and renews it about
30 days before it expires. See `configs/examples/acme.lotus`.

| Directive | Meaning |
|---|---|
| `email` | contact address registered with the CA |
| `ca` | ACME directory URL; default is Let's Encrypt production. Use the staging URL (`https://acme-staging-v02.api.letsencrypt.org/directory`) while testing |
| `storage` | where the account key, certificates and private keys are kept; default `/var/lib/lotus/certs` (`%ProgramData%\lotus\certs` on Windows). Created readable by the LOTUS user only |
| `challenges` | `[ "http-01", "tls-alpn-01" ]` by default; use one when the other port is closed |
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
- Wildcard names (`*.example.com`) are refused in the configuration: they need
  DNS-01. Use `tls manual` with a wildcard certificate meanwhile.

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

If the CA reaches neither port, issuance fails and is retried with growing
delays; the log says which name failed and why.

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

The tests start Pebble and its mock DNS themselves, issue certificates over both
challenges, verify the chain and the name, make real HTTPS requests, check that a
restart reuses the stored certificate, and that a reload adds a site. Without the
variables they are skipped.

## Limits

- The first issuance happens after the listeners are up; there is no option to
  hold start until it completes.
- Renewal failures are logged. There is no command yet that lists certificates
  and expiry dates, and no metric.
- Let's Encrypt has stopped OCSP; LOTUS doesn't staple.
- One ACME account and CA per server.
