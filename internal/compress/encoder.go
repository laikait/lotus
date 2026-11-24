package compress

import (
	"io"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/andybalholm/brotli"
	kgzip "github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

// Level trades speed for size. Responses are compressed while a visitor waits,
// so the default is the fast end of what is worth doing.
type Level int

const (
	LevelDefault Level = iota
	LevelFast
	LevelBest
)

// encoder is what all three codings offer.
type encoder interface {
	io.Writer
	Flush() error
	Close() error
	Reset(io.Writer)
}

type poolKey struct {
	coding string
	level  Level
}

var pools sync.Map // poolKey -> *sync.Pool

func newEncoder(coding string, level Level, dst io.Writer) encoder {
	switch coding {
	case Brotli:
		q := map[Level]int{LevelDefault: 4, LevelFast: 1, LevelBest: 9}[level]
		return brotli.NewWriterLevel(dst, q)
	case Zstd:
		l := map[Level]zstd.EncoderLevel{LevelDefault: zstd.SpeedDefault, LevelFast: zstd.SpeedFastest, LevelBest: zstd.SpeedBetterCompression}[level]
		// One goroutine and a small window: an encoder per request must not cost megabytes.
		w, err := zstd.NewWriter(dst, zstd.WithEncoderLevel(l), zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(1<<20), zstd.WithLowerEncoderMem(true))
		if err != nil { // only invalid options fail, and these are fixed
			panic(err)
		}
		return w
	default:
		l := map[Level]int{LevelDefault: 5, LevelFast: 1, LevelBest: 9}[level]
		w, err := kgzip.NewWriterLevel(dst, l)
		if err != nil {
			panic(err)
		}
		return w
	}
}

// getEncoder returns an encoder that writes to dst, reusing one when possible
// (encoders allocate tens to hundreds of kilobytes).
func getEncoder(coding string, level Level, dst io.Writer) encoder {
	p, _ := pools.LoadOrStore(poolKey{coding, level}, &sync.Pool{})
	if e, ok := p.(*sync.Pool).Get().(encoder); ok {
		e.Reset(dst)
		return e
	}
	return newEncoder(coding, level, dst)
}

// putEncoder gives an encoder back. It must not hold the connection any more.
func putEncoder(coding string, level Level, e encoder) {
	e.Reset(io.Discard)
	p, _ := pools.LoadOrStore(poolKey{coding, level}, &sync.Pool{})
	p.(*sync.Pool).Put(e)
}

// Limiter bounds how many responses are compressed at the same time. A server
// that compresses everything on every core stops answering; past the limit,
// responses go out uncompressed instead of waiting.
type Limiter struct {
	slots chan struct{}
	busy  atomic.Uint64
}

// NewLimiter returns a Limiter for n simultaneous compressions; n <= 0 picks
// twice the number of CPUs (at least 4).
func NewLimiter(n int) *Limiter {
	if n <= 0 {
		n = 2 * runtime.NumCPU()
		if n < 4 {
			n = 4
		}
	}
	return &Limiter{slots: make(chan struct{}, n)}
}

func (l *Limiter) tryAcquire() bool {
	select {
	case l.slots <- struct{}{}:
		return true
	default:
		l.busy.Add(1)
		return false
	}
}

func (l *Limiter) release() { <-l.slots }

// Busy counts responses that went out uncompressed because the limit was reached.
func (l *Limiter) Busy() uint64 { return l.busy.Load() }

// Active is how many compressions are running now.
func (l *Limiter) Active() int { return len(l.slots) }
