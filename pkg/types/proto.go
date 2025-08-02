package types

import (
	"fmt"
	"strings"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	core "k8s.io/api/core/v1"
)

// Proto declares the IP protocol numbers.
// See: https://www.iana.org/assignments/protocol-numbers/protocol-numbers.xhtml
type Proto byte

const (
	ProtoTCP    = Proto(6)
	ProtoUDP    = Proto(17)
	ProtoICMP   = Proto(1)
	ProtoICMPV6 = Proto(58)
	ProtoSCTP   = Proto(132)
)

// ParseProto returns the IP protocol for the given network. Currently only supports
// TCP, UDP, and ICMP.
func ParseProto(network string) (Proto, error) {
	switch strings.ToLower(network) {
	case "", "tcp", "tcp4", "tcp6":
		return ProtoTCP, nil
	case "udp", "udp4", "udp6":
		return ProtoUDP, nil
	case "icmp":
		return ProtoICMP, nil
	case "icmpv6":
		return ProtoICMPV6, nil
	case "sctp":
		return ProtoSCTP, nil
	default:
		return 0, fmt.Errorf("unsupported protocol: %q", network)
	}
}

func FromK8sProtocol(protocol core.Protocol) Proto {
	p, err := ParseProto(string(protocol))
	if err != nil {
		p = ProtoTCP
	}
	return p
}

func (p Proto) String() string {
	switch p {
	case ProtoICMP:
		return "ICMP"
	case ProtoICMPV6:
		return "ICMPv6"
	case 0, ProtoTCP:
		return string(core.ProtocolTCP)
	case ProtoUDP:
		return string(core.ProtocolUDP)
	case ProtoSCTP:
		return string(core.ProtocolSCTP)
	default:
		return fmt.Sprintf("IP-protocol %d", p)
	}
}

func (p Proto) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, p.String())
}

func (p *Proto) UnmarshalJSONFrom(in *jsontext.Decoder) error {
	var s string
	err := json.UnmarshalDecode(in, &s)
	if err == nil {
		*p, err = ParseProto(s)
	}
	return err
}
