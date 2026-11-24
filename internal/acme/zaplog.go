package acme

import (
	"context"
	"log/slog"

	"go.uber.org/zap/zapcore"
)

// slogCore sends certmagic's zap log entries to LOTUS's slog logger.
type slogCore struct {
	l      *slog.Logger
	fields []zapcore.Field
}

func newCore(l *slog.Logger) zapcore.Core { return &slogCore{l: l} }

func (c *slogCore) Enabled(lvl zapcore.Level) bool {
	return c.l.Enabled(context.Background(), slogLevel(lvl))
}

func (c *slogCore) With(fs []zapcore.Field) zapcore.Core {
	return &slogCore{l: c.l, fields: append(append([]zapcore.Field(nil), c.fields...), fs...)}
}

func (c *slogCore) Check(e zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(e.Level) {
		return ce.AddCore(e, c)
	}
	return ce
}

func (c *slogCore) Write(e zapcore.Entry, fs []zapcore.Field) error {
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range c.fields {
		f.AddTo(enc)
	}
	for _, f := range fs {
		f.AddTo(enc)
	}
	args := make([]any, 0, 2*len(enc.Fields)+2)
	if e.LoggerName != "" {
		args = append(args, "logger", e.LoggerName)
	}
	for k, v := range enc.Fields {
		args = append(args, k, v)
	}
	c.l.Log(context.Background(), slogLevel(e.Level), e.Message, args...)
	return nil
}

func (c *slogCore) Sync() error { return nil }

func slogLevel(l zapcore.Level) slog.Level {
	switch {
	case l <= zapcore.DebugLevel:
		return slog.LevelDebug
	case l == zapcore.InfoLevel:
		return slog.LevelInfo
	case l == zapcore.WarnLevel:
		return slog.LevelWarn
	}
	return slog.LevelError
}
