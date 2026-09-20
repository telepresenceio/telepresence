package state

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AuthorizedNamespace returns nil when this session may use namespace ns and
// a PermissionDenied error when it may not, memoizing probe's verdict for the
// session's lifetime. An Unavailable probe result is returned but not cached,
// so a later call retries the probe.
func (cs *ClientSession) AuthorizedNamespace(ns string, probe func() error) error {
	if allowed, ok := cs.authorizedNamespaces.Load(ns); ok {
		if allowed {
			return nil
		}
		return status.Errorf(codes.PermissionDenied, "client session %q is not authorized for namespace %q", cs.sessionID(), ns)
	}
	err := probe()
	if status.Code(err) == codes.Unavailable {
		return err
	}
	cs.authorizedNamespaces.Store(ns, err == nil)
	return err
}
