package iputil

import (
	"encoding/json"
	"net"
	"strings"

	"sigs.k8s.io/kustomize/kyaml/yaml"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func ConvertSubnets(ms []*manager.IPNet) []*net.IPNet {
	ns := make([]*net.IPNet, len(ms))
	for i, m := range ms {
		n := IPNetFromRPC(m)
		ns[i] = n
	}
	return ns
}

func IPNetToRPC(n *net.IPNet) *manager.IPNet {
	ones, _ := n.Mask.Size()
	return &manager.IPNet{
		Ip:   n.IP,
		Mask: int32(ones),
	}
}

func IPNetFromRPC(r *manager.IPNet) *net.IPNet {
	return &net.IPNet{
		IP:   r.Ip,
		Mask: net.CIDRMask(int(r.Mask), len(r.Ip)*8),
	}
}

func IsIpV6Addr(ipAddStr string) bool {
	return strings.Count(ipAddStr, ":") >= 2
}

// Subnet is a net.IPNet that can be marshalled/unmarshalled as yaml or json.
type Subnet net.IPNet

func (s *Subnet) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
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

func (s *Subnet) MarshalYAML() (any, error) {
	return s.String(), nil
}

func (s *Subnet) UnmarshalYAML(node *yaml.Node) error {
	var str string
	if err := node.Decode(&str); err != nil {
		return err
	}
	_, ipNet, err := net.ParseCIDR(str)
	if err != nil {
		return err
	}
	*s = *(*Subnet)(ipNet)
	return nil
}

func (s *Subnet) String() string {
	return (*net.IPNet)(s).String()
}
