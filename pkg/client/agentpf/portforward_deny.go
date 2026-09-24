package agentpf

import (
	"context"
	"errors"
	"net"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/telepresenceio/clog"
)

// errNoDirectAccess is ensureConnectLocked's connectErr when a dial fails for a pod
// already known to refuse pods/portforward: a retry would only repeat the same refusal.
var errNoDirectAccess = errors.New("direct agent access refused (pods/portforward)")

// podKey identifies the pod a port-forward dial targets. RBAC can grant pods/portforward
// per pod via resourceNames, so the denial cache is keyed by pod rather than namespace: a
// refusal for one pod must not deny another pod in the same namespace. uid distinguishes a
// recreated pod that reuses the same name from the one that was actually denied.
type podKey struct {
	namespace string
	name      string
	uid       string
}

// wrapPortForwardDialer wraps dial so a refused port-forward (no pods/portforward RBAC for
// pod) is recorded on owner before the error is returned unchanged, and so a pod already
// known to refuse fails at once without another API request.
func wrapPortForwardDialer(
	owner *clients, pod podKey, dial func(ctx context.Context, address string) (net.Conn, error),
) func(ctx context.Context, address string) (net.Conn, error) {
	return func(ctx context.Context, address string) (net.Conn, error) {
		if owner.isPortForwardDenied(pod) {
			return nil, errNoDirectAccess
		}
		conn, err := dial(ctx, address)
		if err != nil && isPortForwardForbidden(err) {
			owner.markPortForwardDenied(pod)
		}
		return conn, err
	}
}

// isPortForwardForbidden reports whether err is a refused pods/portforward upgrade.
// client-go's port-forward dialers (k8s.io/client-go/tools/portforward,
// k8s.io/streaming/pkg/httpstream/spdy) discard the response's Status object on a
// non-upgrade response and surface only its message text, so a Forbidden refusal
// dialed through them is recognized by the wording the API server always uses for
// it (apierrors.NewForbidden's message format), not by its type.
func isPortForwardForbidden(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsForbidden(err) {
		return true
	}
	return strings.Contains(err.Error(), "forbidden:")
}

// markPortForwardDenied records that a port-forward dial to pod was refused, logging the
// first refusal for that pod.
func (s *clients) markPortForwardDenied(pod podKey) {
	s.portForwardDeniedMu.Lock()
	if s.portForwardDenied == nil {
		s.portForwardDenied = make(map[podKey]struct{})
	}
	_, seen := s.portForwardDenied[pod]
	if !seen {
		s.portForwardDenied[pod] = struct{}{}
	}
	s.portForwardDeniedMu.Unlock()
	if !seen {
		clog.Infof(s, "direct agent access to pod %s.%s refused (pods/portforward); agent traffic goes through the traffic-manager",
			pod.name, pod.namespace)
	}
}

// isPortForwardDenied reports whether a port-forward dial to pod has already been refused
// this session.
func (s *clients) isPortForwardDenied(pod podKey) bool {
	s.portForwardDeniedMu.Lock()
	_, denied := s.portForwardDenied[pod]
	s.portForwardDeniedMu.Unlock()
	return denied
}
