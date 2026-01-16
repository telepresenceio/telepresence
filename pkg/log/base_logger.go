package log

import (
	"context"
	"io"
	"log/slog"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/clog/handler"
)

func MakeBaseLogger(ctx context.Context, out io.Writer, logLevel string) context.Context {
	opts := []handler.Option{
		handler.Output(out),
		handler.TimeFormat("2006-01-02 15:04:05.0000"),
		handler.LevelEnabler(clog.TreeEnabled),
	}
	lvl := slog.LevelInfo
	if logLevel != "" {
		lvl = clog.MustParseLevel(logLevel)
	}
	ctx = clog.WithTreeLevel(ctx, lvl)
	return clog.WithLogger(ctx, slog.New(handler.NewText(opts...)))
}
