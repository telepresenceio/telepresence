package state

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReadAccess is a session's read access to one namespace: all of it, or
// only the workloads that pass the per-workload probe.
type ReadAccess struct {
	cs           *ClientSession
	namespace    string
	all          bool
	workloadFunc func(workload string) error
}

// All reports whether the whole namespace is readable.
func (a ReadAccess) All() bool {
	return a.all
}

// Allows reports whether the named workload is readable. The error is
// non-nil only when the probe is Unavailable; nothing is cached then.
func (a ReadAccess) Allows(workload string) (bool, error) {
	if a.all {
		return true, nil
	}
	if a.cs == nil {
		return false, nil
	}
	key := a.namespace + "/" + workload
	if allowed, ok := a.cs.readableWorkloads.Load(key); ok {
		return allowed, nil
	}
	err := a.workloadFunc(workload)
	if status.Code(err) == codes.Unavailable {
		return false, err
	}
	a.cs.readableWorkloads.Store(key, err == nil)
	return err == nil, nil
}

// ReadAccess returns this session's read access to namespace ns. The
// namespace probe runs until it gives a definite answer, which is then kept
// for the session's lifetime. The workload probe runs only when the
// namespace is denied, once per workload and definite answer. An Unavailable
// namespace probe result is returned as the error and is not cached.
func (cs *ClientSession) ReadAccess(ns string, namespaceProbe func() error, workloadProbe func(workload string) error) (ReadAccess, error) {
	allowed, ok := cs.readableNamespaces.Load(ns)
	if !ok {
		err := namespaceProbe()
		if status.Code(err) == codes.Unavailable {
			return ReadAccess{}, err
		}
		allowed = err == nil
		cs.readableNamespaces.Store(ns, allowed)
	}
	if allowed {
		return ReadAccess{all: true}, nil
	}
	return ReadAccess{cs: cs, namespace: ns, workloadFunc: workloadProbe}, nil
}
