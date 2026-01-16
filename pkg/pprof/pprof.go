package pprof

import (
	"context"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
)

func PprofServer(ctx context.Context, port uint16) error {
	svc := http.Server{
		BaseContext: func(_ net.Listener) context.Context { return ctx },
		Addr:        fmt.Sprintf("localhost:%d", port),
	}
	go func() {
		_ = svc.ListenAndServe()
	}()
	<-ctx.Done()
	return svc.Shutdown(context.Background())
}
