package mutator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	admission "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

const (
	tlsDir          = `/var/run/secrets/tls`
	tlsCertFile     = `crt.pem`
	tlsKeyFile      = `key.pem`
	jsonContentType = `application/json`
)

var universalDeserializer = serializer.NewCodecFactory(runtime.NewScheme()).UniversalDeserializer()

// JSON patch, see https://tools.ietf.org/html/rfc6902 .
type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

type mutatorFunc func(context.Context, *admission.AdmissionRequest) ([]patchOperation, error)

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

	mux := http.NewServeMux()
	mux.HandleFunc("/traffic-agent", func(w http.ResponseWriter, r *http.Request) {
		dlog.Debug(ctx, "Received webhook request...")
		bytes, statusCode, err := serveMutatingFunc(ctx, r, agentInjector)
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
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := &dhttp.ServerConfig{Handler: mux}
	addr := ":" + strconv.Itoa(install.MutatorWebhookPortHTTPS)
	dlog.Infof(ctx, "Mutating webhook service is listening on %v", addr)
	err := server.ListenAndServeTLS(ctx, addr, certPath, keyPath)
	if err != nil {
		err = fmt.Errorf("mutating webhook service stopped. %w", err)
		return err
	}
	dlog.Info(ctx, "Mutating webhook service stopped")
	return nil
}

// Skip mutate requests in these namespaces
func isNamespaceOfInterest(ctx context.Context, ns string) bool {
	for _, skippedNs := range []string{
		metav1.NamespacePublic,
		metav1.NamespaceSystem,
		corev1.NamespaceNodeLease,
	} {
		if ns == skippedNs {
			return false
		}
	}
	return true
}

// serveMutatingFunc is a helper function to call a mutatorFunc.
func serveMutatingFunc(ctx context.Context, r *http.Request, mf mutatorFunc) ([]byte, int, error) {
	// Request validations.
	// Only handle POST requests with a body and json content type.
	if r.Method != http.MethodPost {
		return nil, http.StatusMethodNotAllowed, fmt.Errorf("invalid method %s, only POST requests are allowed", r.Method)
	}

	body, err := ioutil.ReadAll(r.Body)
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
		TypeMeta: metav1.TypeMeta{
			Kind:       "AdmissionReview",
			APIVersion: "admission.k8s.io/v1",
		},
		Response: &response,
	}

	var patchOps []patchOperation
	// Apply the mf() function only namespaces of interest
	if isNamespaceOfInterest(ctx, request.Namespace) {
		patchOps, err = mf(ctx, request)
	}

	if err != nil {
		// If the handler returned an error, still allow the object creation, and incorporate
		// the error message into the response
		dlog.Errorf(ctx, "mutating function error: %v", err)
		response.Allowed = false
		response.Result = &metav1.Status{
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
