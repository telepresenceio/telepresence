package types

import (
	"fmt"
	"strconv"
	"strings"

	core "k8s.io/api/core/v1"
)

type PortMapping string

func NewPortMapping(from PortIdentifier, to uint16) PortMapping {
	p, s, n := from.ProtoAndNameOrNumber()
	switch {
	case s == "" && p == core.ProtocolTCP:
		s = fmt.Sprintf("%d:%d", n, to)
	case s == "":
		s = fmt.Sprintf("%d:%d/%s", n, to, p)
	case p == core.ProtocolTCP:
		s = fmt.Sprintf("%s:%d", s, to)
	default:
		s = fmt.Sprintf("%s:%d/%s", s, to, p)
	}
	return PortMapping(s)
}

func (p PortMapping) String() string {
	return string(p)
}

func (p PortMapping) From() PortIdentifier {
	from, _, _ := p.FromAndTo()
	return from
}

func (p PortMapping) To() PortAndProto {
	_, toAndProto, _ := p.FromAndTo()
	return toAndProto
}

func (p PortMapping) Validate() error {
	_, _, err := p.FromAndTo()
	return err
}

// FromAndTo returns the identifier for the source port and the PortAndProto of the destination port.
// An error is returned if the port-mapping syntax is invalid.
func (p PortMapping) FromAndTo() (from PortIdentifier, to PortAndProto, err error) {
	ps := string(p)
	if sepIdx := strings.Index(ps, ":"); sepIdx > 0 {
		to, err = NewPortAndProto(ps[sepIdx+1:])
		if err == nil {
			from, err = NewPortIdentifier(string(to.Proto), ps[:sepIdx])
		}
	} else {
		to, err = NewPortAndProto(ps)
		if err == nil {
			from = PortIdentifier(ps)
		}
	}
	return
}

// FromNumberAndTo returns source port number and the PortAndProto of the destination port.
// An error is returned if the source port is symbolic or if the port-mapping syntax is invalid.
func (p PortMapping) FromNumberAndTo() (from uint16, to PortAndProto, err error) {
	ps := string(p)
	if sepIdx := strings.Index(ps, ":"); sepIdx > 0 {
		to, err = NewPortAndProto(ps[sepIdx+1:])
		if err == nil {
			var fi uint64
			fi, err = strconv.ParseUint(ps[:sepIdx], 10, 16)
			from = uint16(fi)
		}
	} else {
		to, err = NewPortAndProto(ps)
		if err == nil {
			from = to.Port
		}
	}
	return
}
