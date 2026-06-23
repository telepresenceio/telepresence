package trafficmgr

import (
	"slices"
	"sort"
)

func normalizeMappedNamespaces(namespaces []string) ([]string, bool) {
	if mappedNamespacesAll(namespaces) {
		return nil, true
	}
	namespaces = slices.Clone(namespaces)
	sort.Strings(namespaces)
	return slices.Compact(namespaces), false
}

func mappedNamespacesAll(namespaces []string) bool {
	return len(namespaces) == 1 && namespaces[0] == "all"
}

// effectiveMappedNamespaces resolves the namespaces the client creates top-level
// DNS for, by precedence: an explicit --mapped-namespaces request, then the
// client config, then the traffic-manager's set. "all" (and an empty result)
// means every namespace. A mapped namespace need not be managed by the
// traffic-manager; DNS for any namespace is resolved server-side, so mapping an
// unmanaged namespace is valid.
func effectiveMappedNamespaces(requestedNamespaces, clientNamespaces, managerNamespaces []string) []string {
	requestedNamespaces, requestedAll := normalizeMappedNamespaces(requestedNamespaces)
	clientNamespaces, clientAll := normalizeMappedNamespaces(clientNamespaces)
	managerNamespaces, _ = normalizeMappedNamespaces(managerNamespaces)

	switch {
	case requestedAll:
		return managerNamespaces
	case len(requestedNamespaces) > 0:
		return requestedNamespaces
	case clientAll:
		return managerNamespaces
	case len(clientNamespaces) > 0:
		return clientNamespaces
	default:
		return managerNamespaces
	}
}
