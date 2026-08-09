package portforward

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/resolver"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type PodAddress struct {
	FromSvc   bool
	Name      string
	Namespace string
	Port      uint16
	Proto     types.Proto
	PodID     k8sTypes.UID
}

// NoLookupMarker replaces the pod UID for an address that must resolve with
// no Kubernetes API call (the known-name manager dial). The port-forward
// POST only needs the pod name and namespace; a pod that dies or is
// replaced is detected by connection death, not UID mismatch.
const NoLookupMarker = "!"

// UIDSeparator separates the pod UID (or NoLookupMarker) from the address.
// It is a RFC 3986 unreserved character, so the full address survives the
// url.Parse the grpc resolver machinery applies to its target.
const UIDSeparator = "~"

func parseAddr(fullAddr string) (kind, name, namespace, port string, podID k8sTypes.UID, err error) {
	addr := fullAddr
	if sep := strings.LastIndex(fullAddr, UIDSeparator); sep > 0 {
		id := addr[sep+1:]
		addr = addr[:sep]
		if id == NoLookupMarker {
			podID = NoLookupMarker
		} else if _, err := uuid.Parse(id); err == nil {
			podID = k8sTypes.UID(id)
		}
	}
	if slash := strings.Index(addr, "/"); slash < 0 {
		kind = "pod"
	} else {
		kind = addr[:slash]
		addr = addr[slash+1:]
	}
	if name, port, err = net.SplitHostPort(addr); err == nil {
		var namespace string
		if dot := strings.LastIndex(name, "."); dot > 0 {
			namespace = name[dot+1:]
			name = name[:dot]
		}
		return kind, name, namespace, port, podID, nil
	}
	return "", "", "", "", "", fmt.Errorf("%q is not a valid [<kind>/]<name[.namespace]>:<port-number>[~<uid>]", fullAddr)
}

func parsePodAddr(addr string) (PodAddress, error) {
	kind, name, namespace, port, podId, err := parseAddr(addr)
	if err != nil {
		return PodAddress{}, err
	}
	if podId == NoLookupMarker {
		podId = ""
	}
	if kind == "pod" {
		if pn, err := strconv.ParseUint(port, 10, 16); err == nil {
			return PodAddress{
				Name:      name,
				Namespace: namespace,
				Port:      uint16(pn),
				PodID:     podId,
			}, nil
		}
	}
	return PodAddress{}, fmt.Errorf("%q is not a valid pod port address", addr)
}

func (pa *PodAddress) String() string {
	return pa.AddrFor(pa.Port)
}

// AddrFor formats a k8spf dial address for port, reusing pa's pod identity.
// A PodAddress with no PodID (the known-name, no-lookup form) formats with
// NoLookupMarker so the resolver short-circuits for it too.
func (pa *PodAddress) AddrFor(port uint16) string {
	id := pa.PodID
	if id == "" {
		id = NoLookupMarker
	}
	return fmt.Sprintf("pod/%s.%s:%d%s%s", pa.Name, pa.Namespace, port, UIDSeparator, id)
}

func (pa *PodAddress) state() resolver.State {
	return resolver.State{Addresses: []resolver.Address{{Addr: pa.String()}}}
}
