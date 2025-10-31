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

func parseAddr(fullAddr string) (kind, name, namespace, port string, podID k8sTypes.UID, err error) {
	addr := fullAddr
	if hash := strings.LastIndex(fullAddr, "#"); hash > 0 {
		id := addr[hash+1:]
		addr = addr[:hash]
		if _, err := uuid.Parse(id); err == nil {
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
	return "", "", "", "", "", fmt.Errorf("%q is not a valid [<kind>/]<name[.namespace]>:<port-number>[#<uid>]", fullAddr)
}

func parsePodAddr(addr string) (PodAddress, error) {
	kind, name, namespace, port, podId, err := parseAddr(addr)
	if err != nil {
		return PodAddress{}, err
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
	return fmt.Sprintf("%s.%s:%d#%s", pa.Name, pa.Namespace, pa.Port, pa.PodID)
}

func (pa *PodAddress) state() resolver.State {
	return resolver.State{Addresses: []resolver.Address{{Addr: pa.String()}}}
}
