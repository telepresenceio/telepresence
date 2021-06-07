package iputil

import (
	"encoding/json"
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

type Subnet net.IPNet

func (s *Subnet) MarshalJSON() ([]byte, error) {
	return json.Marshal((*net.IPNet)(s).String())
}

func (s *Subnet) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	_, ipNet, err := net.ParseCIDR(str)
	if err != nil {
		return err
	}
	*s = *(*Subnet)(ipNet)
	return nil
}
