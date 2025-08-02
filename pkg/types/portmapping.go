package types

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type PortMapping string

func NewPortMapping(from PortIdentifier, to uint16) PortMapping {
	p, s, n := from.ProtoAndNameOrNumber()
	switch {
	case s == "" && p == ProtoTCP:
		s = fmt.Sprintf("%d:%d", n, to)
	case s == "":
		s = fmt.Sprintf("%d:%d/%s", n, to, p)
	case p == ProtoTCP:
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

func (p PortMapping) FromAsNumeric() PortAndProto {
	pr, _, n := p.From().ProtoAndNameOrNumber()
	return PortAndProto{
		Port:  n,
		Proto: pr,
	}
}

func (p PortMapping) FromAsIntOrStr() intstr.IntOrString {
	return p.From().AsIntOrStr()
}

func (p PortMapping) To() PortIdentifier {
	_, toAndProto, _ := p.FromAndTo()
	return toAndProto
}

func (p PortMapping) ToAsNumeric() PortAndProto {
	pr, _, n := p.To().ProtoAndNameOrNumber()
	return PortAndProto{
		Port:  n,
		Proto: pr,
	}
}

func (p PortMapping) ToAsIntOrStr() intstr.IntOrString {
	return p.To().AsIntOrStr()
}

func (p PortMapping) Validate() error {
	_, _, err := p.FromAndTo()
	return err
}

func (p *PortMapping) UnmarshalJSONFrom(in *jsontext.Decoder) error {
	var s string
	err := json.UnmarshalDecode(in, &s)
	if err == nil {
		pm := PortMapping(s)
		err = pm.Validate()
		if err == nil {
			*p = pm
		}
	}
	return err
}

// FromAndTo returns the identifier for the source port and the PortAndProto of the destination port.
// An error is returned if the port-mapping syntax is invalid.
func (p PortMapping) FromAndTo() (from PortIdentifier, to PortIdentifier, err error) {
	ps := string(p)
	if sepIdx := strings.IndexByte(ps, ':'); sepIdx > 0 {
		to = PortIdentifier(ps[sepIdx+1:])
		err = to.Validate()
		if err == nil {
			proto, _, _ := to.ProtoAndNameOrNumber()
			from, err = NewPortIdentifier(proto, ps[:sepIdx])
		}
	} else {
		to = PortIdentifier(ps)
		err = to.Validate()
		if err == nil {
			from = PortIdentifier(ps)
			err = from.Validate()
		}
	}
	return
}

// FromNumberAndTo returns source port number and the PortAndProto of the destination port.
// An error is returned if the source port is symbolic or if the port-mapping syntax is invalid.
func (p PortMapping) FromNumberAndTo() (from uint16, to PortAndProto, err error) {
	ps := string(p)
	if sepIdx := strings.IndexByte(ps, ':'); sepIdx > 0 {
		to, err = ParsePortAndProto(ps[sepIdx+1:])
		if err == nil {
			var fi uint64
			fi, err = strconv.ParseUint(ps[:sepIdx], 10, 16)
			from = uint16(fi)
		}
	} else {
		to, err = ParsePortAndProto(ps)
		if err == nil {
			from = to.Port
		}
	}
	return
}
