package pprof

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"

	"github.com/datawire/dlib/dhttp"
)

func PprofServer(ctx context.Context, port uint16) error {
	sc := dhttp.ServerConfig{Handler: http.DefaultServeMux}
	return sc.ListenAndServe(ctx, fmt.Sprintf("localhost:%d", port))
}
