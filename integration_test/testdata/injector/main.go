package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	admission "k8s.io/api/admission/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const certFile = "/certs/tls.crt"
const keyFile = "/certs/tls.key"

func main() {
	if _, err := os.Stat(certFile); err != nil {
		log.Fatalf("Unable to read %s: %v", certFile)
	}
	if _, err := os.Stat(keyFile); err != nil {
		log.Fatalf("Unable to read %s: %v", keyFile)
	}
	srv := &http.Server{Handler: http.DefaultServeMux}
	go func() {
		sigCh := make(chan os.Signal)
		signal.Notify(sigCh, os.Interrupt)
		<-sigCh
		_ = srv.Close()
	}()

	http.HandleFunc("/inject", handleInject)
	l, err := net.Listen("tcp", ":8443")
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	if err = srv.ServeTLS(l, certFile, keyFile); err != nil {
		log.Fatal(err)
	}
}

type patchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

type patchOps []patchOp

func handleInject(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err == nil {
		var arw admission.AdmissionReview
		if err = json.Unmarshal(body, &arw); err == nil {
			review(&arw)
			if body, err = json.Marshal(&arw); err == nil {
				_, _ = w.Write(body)
				return
			}
		}
	}
	log.Println(err)
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintf(w, "%s", err)
}

func review(arw *admission.AdmissionReview) {
	pType := admission.PatchTypeJSONPatch
	response := &admission.AdmissionResponse{
		UID:       arw.Request.UID,
		PatchType: &pType,
	}
	rq := arw.Request
	arw.Request = nil
	arw.Response = response

	switch rq.Operation {
	case admission.Create, admission.Update:
		if pod, patch, err := makePatch(rq); err != nil {
			log.Println(err.Error())
			response.Result = &meta.Status{
				Status:  meta.StatusFailure,
				Message: err.Error(),
			}
		} else {
			log.Printf("%s.%s (%s) patched using %s\n", pod.Name, pod.Namespace, pod.GenerateName, string(patch))
			response.Allowed = true
			response.Patch = patch
			response.AuditAnnotations = map[string]string{
				"mutator": fmt.Sprintf("mutated at %s", time.Now().Format("15:04:05.000")),
			}
		}
	default:
		response.Result = &meta.Status{
			Status:  meta.StatusFailure,
			Message: "unhandled operation: " + string(rq.Operation),
		}
	}
}

func makePatch(ar *admission.AdmissionRequest) (*core.Pod, []byte, error) {
	if ar == nil {
		return nil, nil, errors.New("malformed admission review: request is nil")
	}
	var pod core.Pod
	if err := json.Unmarshal(ar.Object.Raw, &pod); err != nil {
		return nil, nil, fmt.Errorf("unmarshal pod: %v", err)
	}

	const cnName = "itest-patch"
	cns := pod.Spec.Containers
	for i := range cns {
		n := cns[i].Name
		if n == cnName || n == "traffic-agent" {
			return &pod, nil, nil
		}
	}

	cn := core.Container{
		Name:  cnName,
		Image: "jmalloc/echo-server",
		Env: []core.EnvVar{
			{
				Name:  "PORT",
				Value: "9180",
			},
		},
		Ports: []core.ContainerPort{
			{
				Name:          "itest-http",
				ContainerPort: 9180,
				Protocol:      core.ProtocolTCP,
			},
		},
		Resources: core.ResourceRequirements{
			Limits: core.ResourceList{
				core.ResourceCPU:    *resource.NewMilliQuantity(50, resource.DecimalSI),
				core.ResourceMemory: *resource.NewQuantity(128*1024*1024, resource.BinarySI),
			},
		},
		ImagePullPolicy: core.PullIfNotPresent,
	}
	patch, err := json.MarshalIndent(patchOps{
		{
			Op:    "replace",
			Path:  "/spec/containers",
			Value: append([]core.Container{cn}, cns...),
		},
	}, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal patch: %v", err)
	}
	return &pod, patch, err
}
