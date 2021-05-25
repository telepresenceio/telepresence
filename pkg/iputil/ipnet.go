package iputil

import (
	"net"

	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
)

func IPNetToRPC(n *net.IPNet) *daemon.IPNet {
	ones, _ := n.Mask.Size()
	return &daemon.IPNet{
		Ip:   n.IP,
		Mask: int32(ones),
	}
}

func IPNetFromRPC(r *daemon.IPNet) *net.IPNet {
	return &net.IPNet{
		IP:   r.Ip,
		Mask: net.CIDRMask(int(r.Mask), len(r.Ip)*8),
	}
}
