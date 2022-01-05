package install

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/go-multierror"
	corev1 "k8s.io/api/core/v1"

	"github.com/datawire/ambassador/v2/pkg/kates"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func GetPodTemplateFromObject(obj kates.Object) (*kates.PodTemplateSpec, error) {
	var tplSpec *kates.PodTemplateSpec
	switch obj := obj.(type) {
	case *kates.ReplicaSet:
		tplSpec = &obj.Spec.Template
	case *kates.Deployment:
		tplSpec = &obj.Spec.Template
	case *kates.StatefulSet:
		tplSpec = &obj.Spec.Template
	default:
		return nil, ObjErrorf(obj, "unsupported workload kind %q", obj.GetObjectKind().GroupVersionKind().Kind)
	}
	return tplSpec, nil
}

// GetPort finds a port with the given name and returns it.
func GetPort(cn *corev1.Container, portName string) (*corev1.ContainerPort, error) {
	ports := cn.Ports
	for pn := range ports {
		p := &ports[pn]
		if p.Name == portName {
			return p, nil
		}
	}
	return nil, fmt.Errorf("unable to locate port %q in container %q", portName, cn.Name)
}

// GetAppProto determines the application protocol of the given ServicePort. The given AppProtocolStrategy
// used if the port's appProtocol attribute is unset.
func GetAppProto(ctx context.Context, aps client.AppProtocolStrategy, p *corev1.ServicePort) string {
	if p.AppProtocol != nil {
		appProto := *p.AppProtocol
		if appProto != "" {
			dlog.Debugf(ctx, "Using application protocol %q from service appProtocol field", appProto)
			return appProto
		}
	}

	switch aps {
	case client.Http:
		return "http"
	case client.Http2:
		return "http2"
	case client.PortName:
		if p.Name == "" {
			dlog.Debug(ctx, "Unable to derive application protocol from unnamed service port with no appProtocol field")
			break
		}
		pn := p.Name
		if dashPos := strings.IndexByte(pn, '-'); dashPos > 0 {
			pn = pn[:dashPos]
		}
		var appProto string
		switch strings.ToLower(pn) {
		case "http", "https", "grpc", "http2":
			appProto = pn
		case "h2c": // h2c is cleartext HTTP/2
			appProto = "http2"
		case "tls", "h2": // same as https in this context and h2 is HTTP/2 with TLS
			appProto = "https"
		}
		if appProto != "" {
			dlog.Debugf(ctx, "Using application protocol %q derived from port name %q", appProto, p.Name)
			return appProto
		}
		dlog.Debugf(ctx, "Unable to derive application protocol from port name %q", p.Name)
	}
	return ""
}

func ObjErrorf(obj kates.Object, format string, args ...interface{}) error {
	return fmt.Errorf("%s name=%q namespace=%q: %w",
		obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), obj.GetNamespace(),
		fmt.Errorf(format, args...))
}

// AlreadyUndone means that an install action has already been undone, perhaps by manual user action
type AlreadyUndone struct {
	err error
	msg string
}

func (e *AlreadyUndone) Error() string {
	return fmt.Sprintf("%s: %v", e.msg, e.err)
}

func (e *AlreadyUndone) Unwrap() error {
	return e.err
}

func NewAlreadyUndone(err error, msg string) error {
	return &AlreadyUndone{err, msg}
}

// IsAlreadyUndone returns whether the given error -- possibly a multierror -- indicates that all actions have been undone.
func IsAlreadyUndone(err error) bool {
	var undone *AlreadyUndone
	if errors.As(err, &undone) {
		return true
	}
	var multi *multierror.Error
	if !errors.As(err, &multi) {
		return false
	}
	for _, err := range multi.WrappedErrors() {
		if !errors.As(err, &undone) {
			return false
		}
	}
	return true
}
