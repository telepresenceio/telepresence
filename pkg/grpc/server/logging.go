package server

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"

	"github.com/telepresenceio/dlib/v2/dgroup"
	"github.com/telepresenceio/dlib/v2/dlog"
)

func interceptorLogger() logging.Logger {
	return logging.LoggerFunc(func(ctx context.Context, lvl logging.Level, msg string, fields ...any) {
		i := logging.Fields(fields).Iterator()
		for i.Next() {
			k, v := i.At()
			switch k {
			case logging.MethodFieldKey:
				// Don't log the remain ping unless we're tracing
				if v == "Remain" && dlog.MaxLogLevel(ctx) < dlog.LogLevelTrace {
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
				ctx = dlog.WithField(ctx, k, v)
			}
		}
		switch lvl {
		case logging.LevelDebug:
			// We treat debug logging from GRPC as Trace
			dlog.Trace(ctx, msg)
		case logging.LevelInfo:
			// We treat info logging from GRPC as Debug
			dlog.Debug(ctx, msg)
		case logging.LevelWarn:
			dlog.Warn(ctx, msg)
		case logging.LevelError:
			dlog.Error(ctx, msg)
		}
	})
}

func callCtx(ctx context.Context, name string, requestCount *uint64) context.Context {
	if ix := strings.LastIndexByte(name, '/'); ix >= 0 {
		name = name[ix+1:]
	}
	num := atomic.AddUint64(requestCount, 1)
	return dgroup.WithGoroutineName(ctx, fmt.Sprintf("/%s-%d", name, num))
}
