package mutator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	admission "k8s.io/api/admission/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/mutator/agentconfig"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

const (
	tlsDir          = `/var/run/secrets/tls`
	tlsCertFile     = `tls.crt`
	tlsKeyFile      = `tls.key`
	jsonContentType = `application/json`
)

var universalDeserializer = serializer.NewCodecFactory(runtime.NewScheme()).UniversalDeserializer()

// JSON patch, see https://tools.ietf.org/html/rfc6902 .
type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

type patchOps []patchOperation

func (p patchOps) String() string {
	b, _ := json.MarshalIndent(p, "", "  ")
	return string(b)
}

type mutatorFunc func(context.Context, *admission.AdmissionRequest) (patchOps, error)

func ServeMutator(ctx context.Context) error {
	certPath := filepath.Join(tlsDir, tlsCertFile)
	keyPath := filepath.Join(tlsDir, tlsKeyFile)
	missing := ""
	if _, err := os.Stat(certPath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		missing = certPath
	} else if _, err = os.Stat(keyPath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		missing = keyPath
	}
	if missing != "" {
		dlog.Infof(ctx, "%q is not present so mutator service is disabled", missing)
		return nil
	}

	var ai *agentInjector
	mux := http.NewServeMux()
	mux.HandleFunc("/traffic-agent", func(w http.ResponseWriter, r *http.Request) {
		dlog.Debug(ctx, "Received webhook request...")
		bytes, statusCode, err := serveMutatingFunc(ctx, r, ai.inject)
		if err != nil {
			dlog.Errorf(ctx, "error handling webhook request: %v", err)
			w.WriteHeader(statusCode)
			bytes = []byte(err.Error())
		} else {
			dlog.Debug(ctx, "Webhook request handled successfully")
		}
		if _, err = w.Write(bytes); err != nil {
			dlog.Errorf(ctx, "could not write response: %v", err)
		}
	})
	mux.HandleFunc("/uninstall", func(w http.ResponseWriter, r *http.Request) {
		dlog.Debug(ctx, "Received uninstall request...")
		statusCode, err := serveRequest(ctx, r, http.MethodDelete, ai.uninstall)
		if err != nil {
			dlog.Errorf(ctx, "error handling uninstall request: %v", err)
			w.WriteHeader(statusCode)
			_, _ = w.Write([]byte(err.Error()))
		} else {
			dlog.Debug(ctx, "uninstall request handled successfully")
			w.WriteHeader(http.StatusOK)
		}
	})
	mux.HandleFunc("/upgrade-legacy", func(w http.ResponseWriter, r *http.Request) {
		dlog.Debug(ctx, "Received upgrade-legacy request...")
		statusCode, err := serveRequest(ctx, r, http.MethodPost, ai.upgradeLegacy)
		if err != nil {
			dlog.Errorf(ctx, "error handling upgrade-legacy request: %v", err)
			w.WriteHeader(statusCode)
			_, _ = w.Write([]byte(err.Error()))
		} else {
			dlog.Debug(ctx, "upgrade-legacy request handled successfully")
			w.WriteHeader(http.StatusOK)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	env := managerutil.GetEnv(ctx)
	cw, err := agentconfig.Load(ctx, env.ManagerNamespace)
	if err != nil {
		return err
	}
	ai = &agentInjector{agentConfigs: cw}
	dgroup.ParentGroup(ctx).Go("agent-configs", func(ctx context.Context) error {
		dtime.SleepWithContext(ctx, time.Second) // Give the server some time to start
		return cw.Run(ctx)
	})

	server := &dhttp.ServerConfig{Handler: mux}
	addr := ":" + strconv.Itoa(install.MutatorWebhookPortHTTPS)

	dlog.Infof(ctx, "Mutating webhook service is listening on %v", addr)
	defer dlog.Info(ctx, "Mutating webhook service stopped")
	if err = server.ListenAndServeTLS(ctx, addr, certPath, keyPath); err != nil {
		return fmt.Errorf("mutating webhook service stopped. %w", err)
	}
	return nil
}

// Skip mutate requests in these namespaces
func isNamespaceOfInterest(ctx context.Context, ns string) bool {
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

	var patchOps patchOps
	// Apply the mf() function only namespaces of interest
	if isNamespaceOfInterest(ctx, request.Namespace) {
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
