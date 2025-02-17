package portforward

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/resolver"
	"k8s.io/apimachinery/pkg/types"
)

type podAddress struct {
	fromSvc   bool
	name      string
	namespace string
	port      uint16
	podID     types.UID
}

func parseAddr(fullAddr string) (kind, name, namespace, port string, podID types.UID, err error) {
	addr := fullAddr
	if hash := strings.LastIndex(fullAddr, "#"); hash > 0 {
		id := addr[hash+1:]
		addr = addr[:hash]
		if _, err := uuid.Parse(id); err == nil {
			podID = types.UID(id)
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

func parsePodAddr(addr string) (podAddress, error) {
	kind, name, namespace, port, podId, err := parseAddr(addr)
	if err != nil {
		return podAddress{}, err
	}
	if kind == "pod" {
		if pn, err := strconv.ParseUint(port, 10, 16); err == nil {
			return podAddress{
				name:      name,
				namespace: namespace,
				port:      uint16(pn),
				podID:     podId,
			}, nil
		}
	}
	return podAddress{}, fmt.Errorf("%q is not a valid pod port address", addr)
}

func (pa *podAddress) String() string {
	return fmt.Sprintf("%s.%s:%d#%s", pa.name, pa.namespace, pa.port, pa.podID)
}

func (pa *podAddress) state() resolver.State {
	return resolver.State{Addresses: []resolver.Address{{Addr: pa.String()}}}
}
