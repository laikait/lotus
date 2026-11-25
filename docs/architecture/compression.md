# Compression

Status: built. Responses of any site are compressed on the fly with Brotli,
Zstandard or gzip, and files can be served from precompressed copies. Not built:
compression per route, `deflate`, caching of compressed output (a cached page is
compressed again for each client that asks for it), compressing request bodies,
dictionary compression, and compression of responses between LOTUS and a backend.

Text shrinks to a fifth or less. That is less to send, so pages load sooner on slow
links and bandwidth costs less. It is not free: compressing takes CPU, and under
some conditions it weakens secrecy (see Security). So it is **off unless you turn it
on**, and it only touches the media types you list.

## Configuration

A `compression` block turns it on. In `server` it sets the default for every site; in
a `site` it overrides the server's block setting by setting. `enabled false` turns it
off for one site (or for the whole server).

```
server {
    compression {
        algorithms [ "br", "zstd", "gzip" ]   # most preferred first (the default)
        min_size 1kib                          # smaller bodies are sent as they are
        level default                          # fast, default or best
        precompressed true                     # serve file.css.br / .zst / .gz when they exist
    }

    site "example.com" {
        root "/srv/example"
        compression { level fast }             # inherits the rest
    }
    site "downloads.example.com" {
        root "/srv/dl"
        compression { enabled false }
    }
}
```

See `configs/examples/compression.conf`. Only an administrator can use `compression`.

| Directive | Default | Meaning |
|---|---|---|
| `enabled` | `true` | `false` turns compression off for the site (or the server). |
| `algorithms` | `[ "br", "zstd", "gzip" ]` | The codings LOTUS may use. The client's own weights decide first; this order breaks ties. |
| `types` | text, JSON, JavaScript, XML, SVG, TTF/OTF fonts, ... | Media types that are compressed: exact (`"application/json"`) or a family (`"text/*"`), lower case. Anything else is sent as it is. |
| `min_size` | `1kib` | Smallest body that is compressed. At most `1mib`. |
| `level` | `default` | `fast` uses the least CPU, `best` the most (several times `default` for a few percent less data). |
| `precompressed` | `false` | Serve a file's precompressed copy (see below). |

The default `types` are `text/*` (HTML, CSS, plain text, CSV, ...),
`application/json`, `application/ld+json`, `application/manifest+json`,
`application/geo+json`, `application/javascript`, `application/x-javascript`,
`application/xml`, `application/xhtml+xml`, `application/rss+xml`,
`application/atom+xml`, `application/wasm`, `image/svg+xml`, `image/x-icon`,
`image/vnd.microsoft.icon`, `font/ttf`, `font/otf` and
`application/vnd.ms-fontobject`. Images, video, audio, archives and WOFF fonts are
compressed already and are not in it. `text/event-stream` is never compressed, whatever
the list says: an event must reach the client when it happens.

## What is compressed

A response is compressed when all of this holds:

- its media type is in `types`;
- the client's `Accept-Encoding` accepts one of the configured algorithms (`gzip, br`,
  weights such as `br;q=0.5`, `*` and `q=0` are understood);
- it has a body that is not partial: not `1xx`, `204`, `205`, `206` or `304`, and no
  `Content-Range`;
- the backend (or the file handler) has not encoded it already: `Content-Encoding` is
  absent, so a backend that compresses itself is passed through untouched;
- it doesn't say `Cache-Control: no-transform`;
- its body is at least `min_size`. When the length is known (a file, a `Content-Length`)
  this is decided at once. When it isn't (a script's output, a chunked proxy response)
  the first `min_size` bytes are held back until it is clear; a response that ends
  below it goes out as it was. A `Flush` before that means a stream, which starts
  compressing at once and flushes the compressor with it, so a progressive page still
  arrives progressively;
- fewer than a limit of responses are being compressed at that moment. The limit is
  twice the number of CPUs (at least 4). Past it, responses are sent uncompressed
  rather than queued: a busy server gets faster, not slower.

What changes in a compressed response:

- `Content-Encoding` is set, `Content-Length` is removed (the body is chunked) and
  `Accept-Ranges` is removed.
- A strong `ETag` becomes weak (`W/"..."`): the bytes differ from the uncompressed
  representation. Conditional requests still work (`If-None-Match` compares weakly).
- `Vary: Accept-Encoding` is added to every response whose type could have been
  compressed, whether or not this client got a compressed one, so caches in between
  (and LOTUS's own, below) don't hand a compressed body to a client that can't read it.
  Responses of other types get no `Vary`.
- A `HEAD` gets the headers its `GET` would get; with no length known it is not
  compressed.
- A `Range` request for a file gets a part of the file **as it is** (206 is never
  compressed). With `precompressed`, a range is of the precompressed copy, which is what
  the client was told it is getting.

A response that fails halfway (the backend dropped the connection after the first
bytes) is not finished properly: the compressed stream gets no end marker and the
connection is cut, so the client sees an incomplete response, never a truncated "success".

## Precompressed files

With `precompressed true`, a file served from disk is replaced by its copy when one
exists and the client accepts that coding:

```
app.js         app.js.br        app.js.zst        app.js.gz
```

You make the copies when you deploy (`brotli -k app.js`, `zstd -k app.js`,
`gzip -k app.js`); LOTUS spends no CPU per request, and a copy can be compressed
at the highest level. The rules:

- The copy for the client's best accepted coding that exists is used; the others are
  tried in order.
- A copy **older than its file is ignored** (same time counts as current). An empty
  copy or a directory is ignored too. A copy without its file is not a file: the URL is
  a 404.
- Copies are opened by the same code as every file, so the symlink policy, dotfile
  rules and document root apply to them. A copy that is a symlink out of the root is
  refused.
- `Content-Type` is the file's, not the copy's. `ETag` is the copy's own. `Vary:
  Accept-Encoding` is added to every file response of the site while this is on.
- A file without a copy is compressed on the fly as usual (if its type is in `types`).
  A copy is never compressed again.
- A request for `app.js.br` itself is an ordinary file request.
- Precompressed responses are not in the compression metrics.

## Interaction with the cache

Compression sits outside the response cache. The cache stores what the backend
produced (usually uncompressed) and each client gets it compressed as it asks. That
keeps one copy in memory, at the cost of compressing on each hit. A backend that
compresses itself is stored the way it sent it, once per `Accept-Encoding` variant if
it says `Vary: Accept-Encoding`.

## Metrics

| Series | Type | Meaning |
|---|---|---|
| `lotus_http_compressed_responses_total{site,coding}` | counter | responses compressed on the fly |
| `lotus_http_compression_input_bytes_total{site}`, `..._output_bytes_total{site}` | counter | body bytes before and after; output divided by input is the ratio |
| `lotus_compression_active` | gauge | responses being compressed now |
| `lotus_compression_skipped_busy_total` | counter | responses sent uncompressed because the limit was reached; a steady rate means too little CPU for this load |

`lotus config explain` says whether a site compresses and which coding it would use.

## Security

- **BREACH.** If a page contains a secret (a CSRF token) *and* repeats something an
  attacker can choose (a search term), an attacker who can watch the size of the
  compressed response can recover the secret one character at a time. TLS doesn't stop
  it. Compress the types you need, keep secrets out of pages that reflect input, or
  turn compression off for such sites. LOTUS can't tell which pages are at risk.
- **CPU.** Compression is the most expensive thing LOTUS does per response. The
  concurrency limit keeps it from taking the whole machine, `level fast` makes it
  cheaper, and `precompressed` for static files removes it. Responses of a type that
  doesn't shrink (images, archives) are never touched.
- **Memory.** A response of unknown length is held back up to `min_size` (at most
  `1mib`) before it is known to be worth compressing. Each compressor in use holds some
  hundreds of kilobytes; they are reused, and bounded by the concurrency limit.
- Compression never changes what is served for a URL; it only changes how it is
  encoded, and adds `Vary` so caches don't mix the encodings up.
