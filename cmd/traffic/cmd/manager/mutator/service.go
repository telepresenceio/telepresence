package mutator

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	jsonv1 "encoding/json"

	admission "k8s.io/api/admission/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

const jsonContentType = `application/json`

var universalDeserializer = serializer.NewCodecFactory(runtime.NewScheme()).UniversalDeserializer() //nolint:gochecknoglobals // constant

// PatchOperation is a JSON patch, see https://tools.ietf.org/html/rfc6902 .
type PatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

type PatchOps []PatchOperation

func (p PatchOps) String() string {
	b, _ := json.Marshal(p, jsontext.WithIndent("  "))
	return string(b)
}

type mutatorFunc func(context.Context, *admission.AdmissionRequest) (PatchOps, error)

// tlsListener rereads the certificate from the mutator-webhook secret every time
// it creates a TLS connection, thereby ensuring that it uses a certificate that
// is up-to-date with the one used by the webhook caller.
type tlsListener struct {
	sync.Mutex
	ctx         context.Context
	certGetter  InjectorCertGetter
	cert        tls.Certificate
	certPEM     []byte
	keyPEM      []byte
	tcpListener net.Listener
}

func (l *tlsListener) Accept() (net.Conn, error) {
	conn, err := l.tcpListener.Accept()
	if err != nil {
		return conn, err
	}
	return l.tlsConn(conn)
}

func (l *tlsListener) Close() error {
	return l.tcpListener.Close()
}

func (l *tlsListener) Addr() net.Addr {
	return l.tcpListener.Addr()
}

func (l *tlsListener) tlsConn(conn net.Conn) (net.Conn, error) {
	// Because Listener is a convenience function, help out with
	// this too.  This is not possible for the caller to set once
	// we return a *tcp.Conn wrapping an inaccessible net.Conn.
	// If callers don't want this, they can do things the manual
	// way and tweak as needed. But this is what net/http does
	// itself, so copy that. If net/http changes, we can change
	// here too.
	tcpConn := conn.(*net.TCPConn)
	_ = tcpConn.SetKeepAlive(true)
	_ = tcpConn.SetKeepAlivePeriod(3 * time.Minute)

	newCertPEM, newKeyPEM, err := l.certGetter.LoadCert()
	if err != nil {
		return nil, err
	}

	var cert tls.Certificate
	l.Lock()
	if !(bytes.Equal(newCertPEM, l.certPEM) && bytes.Equal(newKeyPEM, l.keyPEM)) {
		clog.Debug(l.ctx, "Replacing certificate")
		cert, err = tls.X509KeyPair(newCertPEM, newKeyPEM)
		if err == nil {
			l.cert = cert
			l.certPEM = newCertPEM
			l.keyPEM = newKeyPEM
		}
	} else {
		cert = l.cert
	}
	l.Unlock()

	if err != nil {
		return nil, fmt.Errorf("failed to create X509 key pair: %v", err)
	}
	return tls.Server(tcpConn, &tls.Config{Certificates: []tls.Certificate{cert}}), nil
}

// NodeAgentReaper deletes every node-agent Job the traffic-manager created,
// the same way AgentInjector.Uninstall rolls back every injected sidecar.
// mutator must not import package state (state imports mutator), so
// manager.go supplies this callback, bound to state.ReapAllNodeAgentJobs,
// only when node-agent mode is enabled; a nil value skips the reap.
type NodeAgentReaper func(ctx context.Context) error

// uninstallHandler returns the /uninstall handler shared by ServeMutator's
// TLS server and ServeNodeAgentUninstall's plain-HTTP server. It runs both
// the injector's sidecar rollback (ai.Uninstall, skipped when ai is nil, as
// it is for a node-agent-only install with no webhook) and the node-agent
// Job reap (reap, skipped when nil, as it is when node-agent mode is
// disabled). Either action's failure must not block the other: a failed Job
// reap must not block sidecar rollback, and vice versa, so reap's error is
// logged rather than surfaced to the caller -- the hook already treats the
// request as best-effort (`|| exit 0`).
func uninstallHandler(ai AgentInjector, reap NodeAgentReaper) http.HandlerFunc {
	return uninstallHandlerWithContext(nil, ai, reap) //nolint:staticcheck // nil keeps teardown bound to the request context
}

// uninstallHandlerWithContext keeps teardown attached to managerCtx when it
// is provided. The Helm hook can disconnect before sidecars finish rolling
// back, and request cancellation must not abandon that reconciliation.
func uninstallHandlerWithContext(managerCtx context.Context, ai AgentInjector, reap NodeAgentReaper) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestCtx := r.Context()
		actionCtx := requestCtx
		if managerCtx != nil {
			actionCtx = managerCtx
		}
		clog.Debug(requestCtx, "Received uninstall request...")
		statusCode, err := serveRequest(actionCtx, r, http.MethodDelete, func(ctx context.Context) {
			if ai != nil {
				ai.Uninstall(ctx)
			}
			if reap != nil {
				if rErr := reap(ctx); rErr != nil {
					clog.Errorf(ctx, "unable to reap node-agent jobs: %v", rErr)
				}
			}
		})
		if err != nil {
			clog.Errorf(requestCtx, "error handling uninstall request: %v", err)
			w.WriteHeader(statusCode)
			_, _ = w.Write([]byte(err.Error()))
		} else {
			clog.Debug(requestCtx, "uninstall request handled successfully")
			w.WriteHeader(http.StatusOK)
		}
	}
}

func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func ServeMutator(ctx context.Context, g log.Group, injectorCertGetter InjectorCertGetter, reapNodeAgentJobs NodeAgentReaper) error {
	cw := GetMap(ctx)
	ai := NewAgentInjectorFunc(ctx, cw)

	mux := http.NewServeMux()
	mux.HandleFunc("/traffic-agent", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		rsp, statusCode, err := serveMutatingFunc(ctx, r, ai.Inject)
		h := w.Header()
		if err != nil {
			clog.Errorf(ctx, "error handling webhook request: %v", err)
			w.WriteHeader(statusCode)
			rsp = []byte(err.Error())
		} else {
			h.Set("Content-Type", "application/json")
		}
		h.Set("Content-Length", strconv.Itoa(len(rsp)))
		if _, err = w.Write(rsp); err != nil {
			clog.Errorf(ctx, "could not write response: %v", err)
		}
	})
	mux.HandleFunc("/uninstall", uninstallHandlerWithContext(ctx, ai, reapNodeAgentJobs))
	mux.HandleFunc("/healthz", healthzHandler)

	port := managerutil.GetEnv(ctx).MutatorWebhookPort
	lg := clog.StdLogger(ctx, slog.LevelInfo)
	lg.SetPrefix(fmt.Sprintf("%d/", port))

	// Filter this message. It's harmless and caused by the kube-apiserver dropping the connection
	// prematurely. It is always retried.
	lg.SetOutput(&logFilter{
		rx: regexp.MustCompile(`http: TLS handshake error from .*: EOF\s*\z`),
		wr: lg.Writer(),
	})
	server := http.Server{
		Handler:  mux,
		ErrorLog: lg,
		BaseContext: func(n net.Listener) context.Context {
			return ctx
		},
	}
	injectorReady := make(chan error)
	g.Go("agent-configs", func(ctx context.Context) error {
		if _, ok := <-injectorReady; ok {
			// An error was posted on the injectorReady channel. We don't report the
			// error from here, but we refrain from waiting on the watchers.
			return nil
		}
		// the injectorReady was closed with no errors.
		return cw.Wait(ctx)
	})
	return serveAndWatchTLS(ctx, &server, fmt.Sprintf(":%d", port), injectorCertGetter, injectorReady)
}

// ServeNodeAgentUninstall serves only /uninstall and /healthz over plain
// HTTP, for a node-agent-only install (nodeAgent.enabled=true,
// agentInjector.enabled=false): the webhook server never runs in that mode,
// so the pre-delete hook has nothing to reach for the node-agent Job reap
// unless something else listens. It uses no TLS because the injector's
// certificate secret does not exist on such an install. It listens on the
// same port the webhook server would bind (managerutil.Env.MutatorWebhookPort,
// which the chart's agent-injector Service always targets by name
// regardless of which mode created the listening process).
func ServeNodeAgentUninstall(ctx context.Context, reapNodeAgentJobs NodeAgentReaper) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/uninstall", uninstallHandlerWithContext(ctx, nil, reapNodeAgentJobs))
	mux.HandleFunc("/healthz", healthzHandler)

	port := managerutil.GetEnv(ctx).MutatorWebhookPort
	lg := clog.StdLogger(ctx, slog.LevelInfo)
	lg.SetPrefix(fmt.Sprintf("%d/", port))
	server := &http.Server{
		Handler:  mux,
		ErrorLog: lg,
		BaseContext: func(n net.Listener) context.Context {
			return ctx
		},
	}

	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	defer clog.Debug(ctx, "node-agent uninstall service stopped")
	clog.Debug(ctx, "node-agent uninstall service started")

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			clog.Errorf(ctx, "failed to serve: %v", err)
		}
	}()
	<-ctx.Done()
	return server.Shutdown(ctx)
}

type logFilter struct {
	wr io.Writer
	rx *regexp.Regexp
}

func (l *logFilter) Write(data []byte) (int, error) {
	if l.rx.Match(data) {
		return len(data), nil
	}
	return l.wr.Write(data)
}

func serveAndWatchTLS(ctx context.Context, s *http.Server, addr string, certGetter InjectorCertGetter, rdy chan error) (err error) {
	var rdyClose sync.Once
	defer func() {
		if err != nil {
			rdy <- err
		}
		rdyClose.Do(func() { close(rdy) })
	}()

	certPEM, keyPEM, err := certGetter.LoadCert()
	if err != nil {
		return err
	}
	defer clog.Debug(ctx, "service stopped")
	clog.Debug(ctx, "service started")

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("failed to create TLS listener: %v", err)
	}
	lc := net.ListenConfig{}
	tcpListener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}

	go func() {
		err := s.Serve(
			&tlsListener{
				ctx:         ctx,
				certGetter:  certGetter,
				cert:        cert,
				certPEM:     certPEM,
				keyPEM:      keyPEM,
				tcpListener: tcpListener,
			},
		)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			clog.Errorf(ctx, "failed to serve: %v", err)
		}
	}()

	// Give the http server some time to start accepting calls from the listener. We don't want
	// our own rollouts to happen before we are able to receive events from the mutating webhook.
	time.Sleep(3 * time.Second)
	rdyClose.Do(func() { close(rdy) })
	<-ctx.Done()
	return s.Shutdown(ctx)
}

// Skip mutate requests in these namespaces.
func isNamespaceOfInterest(ns string) bool {
	for _, skippedNs := range []string{
		meta.NamespacePublic,
		meta.NamespaceSystem,
		core.NamespaceNodeLease,
	} {
		if ns == skippedNs {
			return false
		}
	}
	return true
}

func serveRequest(ctx context.Context, r *http.Request, method string, f func(ctx context.Context)) (int, error) {
	if r.Method != method {
		return http.StatusMethodNotAllowed, fmt.Errorf("invalid method %s, only %s requests are allowed", r.Method, method)
	}
	f(ctx)
	return 0, nil
}

// serveMutatingFunc is a helper function to call a mutatorFunc.
func serveMutatingFunc(ctx context.Context, r *http.Request, mf mutatorFunc) ([]byte, int, error) {
	// Request validations.
	// Only handle POST requests with a body and json content type.
	if r.Method != http.MethodPost {
		return nil, http.StatusMethodNotAllowed, fmt.Errorf("invalid method %s, only POST requests are allowed", r.Method)
	}

	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("could not read request body: %w", err)
	}

	if contentType := r.Header.Get("Content-Type"); contentType != jsonContentType {
		return nil, http.StatusBadRequest, fmt.Errorf("unsupported content type %s, only %s is supported", contentType, jsonContentType)
	}

	// Parse the AdmissionReview request.
	var admissionReviewReq admission.AdmissionReview

	if _, _, err := universalDeserializer.Decode(body, nil, &admissionReviewReq); err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("could not deserialize request: %v", err)
	}
	request := admissionReviewReq.Request
	if request == nil {
		return nil, http.StatusBadRequest, errors.New("malformed admission review: request is nil")
	}

	// Construct the AdmissionReview response.
	response := admission.AdmissionResponse{
		UID:     request.UID,
		Allowed: true,
	}
	admissionReviewResponse := admission.AdmissionReview{
		TypeMeta: meta.TypeMeta{
			Kind:       "AdmissionReview",
			APIVersion: "admission.k8s.io/v1",
		},
		Response: &response,
	}

	var patchOps PatchOps
	// Apply the mf() function only namespaces of interest
	if isNamespaceOfInterest(request.Namespace) {
		patchOps, err = mf(ctx, request)
	}

	if err != nil {
		clog.Errorf(ctx, "mutating function error: %v", err)
		applyMutatorError(&response, err)
	} else if patchOps != nil {
		// Otherwise, encode the patch operations to JSON and return a positive response.
		patchBytes, err := json.Marshal(patchOps, jsonv1.OmitEmptyWithLegacySemantics(true), json.FormatNilSliceAsNull(true))
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("could not marshal JSON patch: %v", err)
		}
		response.Patch = patchBytes
		patchType := admission.PatchTypeJSONPatch
		response.PatchType = &patchType
	}

	// Return the AdmissionReview with a response as JSON.
	b, err := json.Marshal(&admissionReviewResponse)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("marshaling response: %v", err)
	}
	return b, http.StatusOK, nil
}

// applyMutatorError records a failed mutation on the admission response. The handling depends on
// whether the error is terminal:
//
//   - A User error is the caller's mistake (for example an invalid annotation value) that retrying
//     cannot fix. Denying the request would make the ReplicaSet controller retry the pod creation
//     indefinitely and wedge the workload's rollout. The pod is therefore admitted (without an
//     agent) and the reason is surfaced as a warning.
//   - Any other error is treated as transient (for example the agent config could not be generated
//     yet because the traffic-manager just started). The request is denied so that the pod creation
//     is retried and the agent is injected once the condition clears.
func applyMutatorError(response *admission.AdmissionResponse, err error) {
	if errcat.GetCategory(err) == errcat.User {
		response.Warnings = append(response.Warnings, err.Error())
		return
	}
	response.Allowed = false
	response.Result = &meta.Status{
		Message: err.Error(),
	}
}
