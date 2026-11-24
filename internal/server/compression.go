package server

import (
	"net/http"

	"github.com/laikait/lotus/internal/compress"
	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/metrics"
)

// Response compression (docs/architecture/compression.md).
//
// A site with a `compression` block wraps its response writer in a
// compress.Writer. Everything the site answers goes through it (files, proxied
// responses, PHP, `return`); it compresses what is worth compressing and
// leaves the rest alone. Files can also be served from precompressed copies,
// which the static file handler does itself.

var codingIndex = map[string]int{compress.Brotli: 0, compress.Zstd: 1, compress.Gzip: 2}

// compressConfig builds the settings of a site's writer, or nil when the site
// doesn't compress.
func (s *Server) compressConfig(site *model.Site, st *siteStats) *compress.Config {
	c := site.Compression
	if c == nil {
		return nil
	}
	level := compress.LevelDefault
	switch c.Level {
	case "fast":
		level = compress.LevelFast
	case "best":
		level = compress.LevelBest
	}
	return &compress.Config{
		Algorithms: c.Algorithms,
		Types:      c.Types,
		MinSize:    c.MinSize,
		Level:      level,
		Limiter:    s.compLimiter,
		Observe:    st.observeCompressed,
	}
}

// compressed wraps w for a request, and returns what to call when the handler
// is done: it ends the compressed stream, or abandons it when the handler
// panicked (a response that failed half way must not look complete).
func (rt *siteRuntime) compressed(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, func()) {
	if rt.compress == nil {
		return w, func() {}
	}
	cw := compress.New(w, r, rt.compress)
	return cw, func() {
		if p := recover(); p != nil {
			cw.Abort()
			panic(p)
		}
		_ = cw.Close()
	}
}

// observeCompressed records a response that was compressed.
func (st *siteStats) observeCompressed(coding string, in, out int64) {
	if i, ok := codingIndex[coding]; ok {
		st.compressed[i].Add(1)
	}
	if in > 0 {
		st.compIn.Add(uint64(in))
	}
	if out > 0 {
		st.compOut.Add(uint64(out))
	}
}

// writeCompressionMetrics reports compression, by site.
func (s *Server) writeCompressionMetrics(w *metrics.Writer, sites []namedStats) {
	w.Family("lotus_http_compressed_responses_total", "counter", "Responses compressed on the fly, by site and coding. Precompressed files are not counted.")
	for _, n := range sites {
		for coding, i := range codingIndex {
			if v := n.st.compressed[i].Load(); v > 0 {
				w.Sample(float64(v), metrics.L("site", n.name), metrics.L("coding", coding))
			}
		}
	}
	w.Family("lotus_http_compression_input_bytes_total", "counter", "Body bytes before compression, of the responses that were compressed, by site.")
	for _, n := range sites {
		if v := n.st.compIn.Load(); v > 0 {
			w.Sample(float64(v), metrics.L("site", n.name))
		}
	}
	w.Family("lotus_http_compression_output_bytes_total", "counter", "Body bytes after compression, by site. Divide by the input to get the ratio.")
	for _, n := range sites {
		if v := n.st.compOut.Load(); v > 0 {
			w.Sample(float64(v), metrics.L("site", n.name))
		}
	}
	w.Gauge("lotus_compression_active", "Responses being compressed now.", float64(s.compLimiter.Active()))
	w.Counter("lotus_compression_skipped_busy_total", "Responses sent uncompressed because too many were being compressed at once.", float64(s.compLimiter.Busy()))
}

type namedStats struct {
	name string
	st   *siteStats
}
