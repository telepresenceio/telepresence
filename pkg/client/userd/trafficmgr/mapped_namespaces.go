package trafficmgr

import (
	"slices"
	"sort"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
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

func effectiveMappedNamespaces(requestedNamespaces, clientNamespaces, managerNamespaces []string) ([]string, error) {
	requestedNamespaces, requestedAll := normalizeMappedNamespaces(requestedNamespaces)
	clientNamespaces, clientAll := normalizeMappedNamespaces(clientNamespaces)
	managerNamespaces, _ = normalizeMappedNamespaces(managerNamespaces)

	var namespaces []string
	switch {
	case requestedAll:
		return managerNamespaces, nil
	case len(requestedNamespaces) > 0:
		namespaces = requestedNamespaces
	case clientAll:
		return managerNamespaces, nil
	case len(clientNamespaces) > 0:
		namespaces = clientNamespaces
	case len(managerNamespaces) > 0:
		return managerNamespaces, nil
	}
	if err := ensureMappedNamespacesManaged(namespaces, managerNamespaces); err != nil {
		return nil, err
	}
	return namespaces, nil
}

func ensureMappedNamespacesManaged(requestedNamespaces, managerNamespaces []string) error {
	if len(requestedNamespaces) == 0 || len(managerNamespaces) == 0 {
		return nil
	}

	managed := make(map[string]struct{}, len(managerNamespaces))
	for _, namespace := range managerNamespaces {
		managed[namespace] = struct{}{}
	}

	var unmanaged []string
	for _, namespace := range requestedNamespaces {
		if _, ok := managed[namespace]; !ok {
			unmanaged = append(unmanaged, namespace)
		}
	}
	if len(unmanaged) == 0 {
		return nil
	}

	return errcat.User.Newf(
		"mapped namespaces %q are not managed by this traffic-manager; managed namespaces are %q. "+
			"Reconnect with --mapped-namespaces limited to managed namespaces, or reconfigure the traffic-manager to also manage %q",
		unmanaged, managerNamespaces, unmanaged,
	)
}
