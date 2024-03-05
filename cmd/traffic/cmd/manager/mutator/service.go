package mutator

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	admission "k8s.io/api/admission/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
)

const (
	tlsCertFile     = `tls.crt`
	tlsKeyFile      = `tls.key`
	jsonContentType = `application/json`
)

var universalDeserializer = serializer.NewCodecFactory(runtime.NewScheme()).UniversalDeserializer() //nolint:gochecknoglobals // constant

// JSON patch, see https://tools.ietf.org/html/rfc6902 .
type PatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

type PatchOps []PatchOperation

func (p PatchOps) String() string {
	b, _ := json.MarshalIndent(p, "", "  ")
	return string(b)
}

type mutatorFunc func(context.Context, *admission.AdmissionRequest) (PatchOps, error)

// tlsListener rereads the certificate from the mutator-webhook secret every time
// it creates a TLS connection, thereby ensuring that it uses a certificate that
// is up-to-date with the one used by the webhook caller.
type tlsListener struct {
	sync.Mutex
	ctx         context.Context
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

	newCertPEM, newKeyPEM, err := loadCert(l.ctx)
	if err != nil {
		return nil, err
	}

	var cert tls.Certificate
	l.Lock()
	if !(bytes.Equal(newCertPEM, l.certPEM) && bytes.Equal(newKeyPEM, l.keyPEM)) {
		dlog.Debug(l.ctx, "Replacing certificate")
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

func ServeMutator(ctx context.Context) error {
	var ai AgentInjector
	mux := http.NewServeMux()
	mux.HandleFunc("/traffic-agent", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		rsp, statusCode, err := serveMutatingFunc(ctx, r, ai.Inject)
		h := w.Header()
		if err != nil {
			dlog.Errorf(ctx, "error handling webhook request: %v", err)
			w.WriteHeader(statusCode)
			rsp = []byte(err.Error())
		} else {
			h.Set("Content-Type", "application/json")
		}
		h.Set("Content-Length", strconv.Itoa(len(rsp)))
		if _, err = w.Write(rsp); err != nil {
			dlog.Errorf(ctx, "could not write response: %v", err)
		}
	})
	mux.HandleFunc("/uninstall", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		dlog.Debug(ctx, "Received uninstall request...")
		statusCode, err := serveRequest(ctx, r, http.MethodDelete, ai.Uninstall)
		if err != nil {
			dlog.Errorf(ctx, "error handling uninstall request: %v", err)
			w.WriteHeader(statusCode)
			_, _ = w.Write([]byte(err.Error()))
		} else {
			dlog.Debug(ctx, "uninstall request handled successfully")
			w.WriteHeader(http.StatusOK)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	cw := GetMap(ctx)
	ai = NewAgentInjectorFunc(ctx, cw)
	dgroup.ParentGroup(ctx).Go("agent-configs", func(ctx context.Context) error {
		dtime.SleepWithContext(ctx, time.Second) // Give the server some time to start
		return cw.Wait(ctx)
	})

	wrapped := otelhttp.NewHandler(mux, "agent-injector", otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
		return operation + r.URL.Path
	}))
	port := managerutil.GetEnv(ctx).MutatorWebhookPort
	lg := dlog.StdLogger(ctx, dlog.MaxLogLevel(ctx))
	lg.SetPrefix(fmt.Sprintf("%d/", port))

	// Filter this message. It's harmless and caused by the kube-apiserver dropping the connection
	// prematurely. It is always retried.
	lg.SetOutput(&logFilter{
		rx: regexp.MustCompile(`http: TLS handshake error from .*: EOF\s*\z`),
		wr: lg.Writer(),
	})
	server := http.Server{
		Handler:  wrapped,
		ErrorLog: lg,
		BaseContext: func(n net.Listener) context.Context {
			return ctx
		},
	}
	return serveAndWatchTLS(ctx, &server, fmt.Sprintf(":%d", port))
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

func serveAndWatchTLS(ctx context.Context, s *http.Server, addr string) error {
	certPEM, keyPEM, err := loadCert(ctx)
	if err != nil {
		return err
	}
	defer dlog.Debug(ctx, "service stopped")
	dlog.Debug(ctx, "service started")

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("failed to create TLS listener: %v", err)
	}
	lc := net.ListenConfig{}
	tcpListener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}

	errc := make(chan error, 1)
	go func() {
		<-ctx.Done()
		errc <- s.Shutdown(dcontext.HardContext(ctx))
	}()

	err = s.Serve(
		&tlsListener{
			ctx:         ctx,
			cert:        cert,
			certPEM:     certPEM,
			keyPEM:      keyPEM,
			tcpListener: tcpListener,
		},
	)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("failed to serve: %v", err)
	}

	return <-errc
}

func loadCert(ctx context.Context) (cert, key []byte, err error) {
	env := managerutil.GetEnv(ctx)
	ns := env.ManagerNamespace
	sn := env.AgentInjectorSecret
	s, err := k8sapi.GetK8sInterface(ctx).CoreV1().Secrets(ns).Get(ctx, sn, meta.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get %s.%s: %w", sn, ns, err)
	}
	return s.Data[tlsCertFile], s.Data[tlsKeyFile], nil
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
	defer func() {
		if r := recover(); r != nil {
			dlog.Errorf(ctx, "%+v", derror.PanicToError(r))
		}
	}()
	if r.Method != method {
		return http.StatusMethodNotAllowed, fmt.Errorf("invalid method %s, only %s requests are allowed", r.Method, method)
	}
	f(ctx)
	return 0, nil
}

// serveMutatingFunc is a helper function to call a mutatorFunc.
func serveMutatingFunc(ctx context.Context, r *http.Request, mf mutatorFunc) ([]byte, int, error) {
	defer func() {
		if r := recover(); r != nil {
			dlog.Errorf(ctx, "%+v", derror.PanicToError(r))
		}
	}()

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
		// If the handler returned an error, still allow the object creation, and incorporate
		// the error message into the response
		dlog.Errorf(ctx, "mutating function error: %v", err)
		response.Allowed = false
		response.Result = &meta.Status{
			Message: err.Error(),
		}
	} else {
		// Otherwise, encode the patch operations to JSON and return a positive response.
		patchBytes, err := json.Marshal(patchOps)
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("could not marshal JSON patch: %v", err)
		}
		response.Patch = patchBytes
		patchType := admission.PatchTypeJSONPatch
		response.PatchType = &patchType
	}

	// Return the AdmissionReview with a response as JSON.
	bytes, err := json.Marshal(&admissionReviewResponse)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("marshaling response: %v", err)
	}
	return bytes, http.StatusOK, nil
}
