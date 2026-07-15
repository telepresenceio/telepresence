package quicforwarder

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"
)

// healthShutdownTimeout bounds how long ServeHealth waits for its HTTP server to finish
// any in-flight request once ctx is done. The health endpoint only ever does an atomic
// bool read, so this is generous headroom, not a real budget.
const healthShutdownTimeout = 2 * time.Second

// healthHandler returns the /healthz handler backing the Deployment's readiness probe.
// Readiness means "the first backend-allowlist snapshot has been received" (per
// Allowlist.Ready), not merely "process up": until that snapshot arrives the forwarder
// drops every datagram, so a pod the Service routes to before then would silently
// blackhole traffic. Split out from ServeHealth so it can be exercised with
// httptest.NewServer/net/http/httptest.ResponseRecorder without binding a real port.
func healthHandler(allowlist *Allowlist) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if allowlist.Ready() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("no backend allowlist snapshot received yet"))
	})
	return mux
}

// ServeHealth runs the forwarder's HTTP health endpoint on all interfaces at port until
// ctx is done, at which point it shuts the server down and returns nil. It exists solely
// to back the Deployment's readiness probe (see Env.HealthPort's doc comment): the probe
// must not pass until allowlist has a snapshot, since a "ready" pod that is still
// dropping every datagram is worse than one the Service hasn't started routing to yet.
func ServeHealth(ctx context.Context, port uint16, allowlist *Allowlist) error {
	srv := &http.Server{
		Addr:    net.JoinHostPort("", strconv.Itoa(int(port))),
		Handler: healthHandler(allowlist),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), healthShutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		<-errCh
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
