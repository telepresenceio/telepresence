package agentpf

import (
	"context"
	"errors"
	"net"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/telepresenceio/clog"
)

// errNoDirectAccess is ensureConnectLocked's connectErr when a dial fails in a namespace
// already known to refuse pods/portforward: a retry would only repeat the same refusal.
var errNoDirectAccess = errors.New("direct agent access refused (pods/portforward)")

// wrapPortForwardDialer wraps dial so a refused port-forward (no pods/portforward RBAC in
// ns) is recorded on owner before the error is returned unchanged, and so a namespace
// already known to refuse fails at once without another API request.
func wrapPortForwardDialer(
	owner *clients, ns string, dial func(ctx context.Context, address string) (net.Conn, error),
) func(ctx context.Context, address string) (net.Conn, error) {
	return func(ctx context.Context, address string) (net.Conn, error) {
		if owner.isPortForwardDenied(ns) {
			return nil, errNoDirectAccess
		}
		conn, err := dial(ctx, address)
		if err != nil && isPortForwardForbidden(err) {
			owner.markPortForwardDenied(ns)
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

// markPortForwardDenied records that a port-forward dial in namespace was refused, logging
// the first refusal for that namespace.
func (s *clients) markPortForwardDenied(namespace string) {
	s.portForwardDeniedMu.Lock()
	if s.portForwardDenied == nil {
		s.portForwardDenied = make(map[string]struct{})
	}
	_, seen := s.portForwardDenied[namespace]
	if !seen {
		s.portForwardDenied[namespace] = struct{}{}
	}
	s.portForwardDeniedMu.Unlock()
	if !seen {
		clog.Infof(s, "direct agent access in namespace %s refused (pods/portforward); agent traffic goes through the traffic-manager", namespace)
	}
}

// isPortForwardDenied reports whether a port-forward dial in namespace has already been
// refused this session.
func (s *clients) isPortForwardDenied(namespace string) bool {
	s.portForwardDeniedMu.Lock()
	_, denied := s.portForwardDenied[namespace]
	s.portForwardDeniedMu.Unlock()
	return denied
}
