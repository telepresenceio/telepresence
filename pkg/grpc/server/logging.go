package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"

	"github.com/telepresenceio/clog"
)

func interceptorLogger() logging.Logger {
	return logging.LoggerFunc(func(ctx context.Context, lvl logging.Level, msg string, fields ...any) {
		lfs := logging.Fields(fields)
		attrs := make([]slog.Attr, 0, len(lfs)/2)
		i := lfs.Iterator()
		for i.Next() {
			k, v := i.At()
			switch k {
			case logging.MethodFieldKey:
				// Don't log the remain ping unless we're tracing
				if v == "Remain" && !clog.Enabled(ctx, clog.LevelTrace) {
					return
				}
			case
				logging.ComponentFieldKey,
				logging.ServiceFieldKey,
				logging.MethodTypeFieldKey,
				"grpc.request.deadline",
				"grpc.start_time",
				"grpc.time_ms",
				"peer.address",
				"protocol":
			default:
				attrs = append(attrs, slog.Any(k, v))
			}
		}
		switch lvl {
		case logging.LevelDebug:
			// We treat debug logging from GRPC as Trace
			clog.TraceAttrs(ctx, msg, attrs...)
		case logging.LevelInfo:
			// We treat info logging from GRPC as Debug
			clog.DebugAttrs(ctx, msg, attrs...)
		case logging.LevelWarn:
			clog.WarnAttrs(ctx, msg, attrs...)
		case logging.LevelError:
			clog.ErrorAttrs(ctx, msg, attrs...)
		}
	})
}

func callCtx(ctx context.Context, name string, requestCount *uint64) context.Context {
	if ix := strings.LastIndexByte(name, '/'); ix >= 0 {
		name = name[ix+1:]
	}
	num := atomic.AddUint64(requestCount, 1)
	return clog.WithGroup(ctx, fmt.Sprintf("%s-%d", name, num))
}
