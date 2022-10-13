package agentconfig

import (
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"

	core "k8s.io/api/core/v1"
)

var ErrNotInteger = errors.New("not an integer")

const ProtoSeparator = byte('/')

// ParseNumericPort parses the given string into a positive unsigned 16-bit integer.
// ErrNotInteger is returned if the string doesn't represent an integer.
// A range error is return unless the integer is between 1 and 65535.
func ParseNumericPort(portStr string) (uint16, error) {
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, ErrNotInteger
	}
	if port < 1 || port > math.MaxUint16 {
		return 0, fmt.Errorf("%s is not between 1 and 65535", portStr)
	}
	return uint16(port), nil
}

func ParseProtocol(protocol string) (core.Protocol, error) {
	pr := core.Protocol(strings.ToUpper(protocol))
	switch pr {
	case "":
		return core.ProtocolTCP, nil
	case core.ProtocolUDP, core.ProtocolTCP:
		return pr, nil
	default:
		return "", fmt.Errorf("unsupported protocol: %s", pr)
	}
}

type PortAndProto struct {
	Port  uint16
	Proto core.Protocol
}

func NewPortAndProto(s string) (PortAndProto, error) {
	pp := PortAndProto{Proto: core.ProtocolTCP}
	var err error
	if ix := strings.IndexByte(s, ProtoSeparator); ix > 0 {
		if pp.Proto, err = ParseProtocol(s[ix+1:]); err != nil {
			return pp, err
		}
		s = s[0:ix]
	}
	pp.Port, err = ParseNumericPort(s)
	return pp, err
}

func (pp *PortAndProto) Addr() (addr net.Addr, err error) {
	as := fmt.Sprintf(":%d", pp.Port)
	if pp.Proto == core.ProtocolTCP {
		addr, err = net.ResolveTCPAddr("tcp", as)
	} else {
		addr, err = net.ResolveUDPAddr("udp", as)
	}
	return
}

func (pp *PortAndProto) String() string {
	if pp.Proto == core.ProtocolTCP {
		return strconv.Itoa(int(pp.Port))
	}
	return fmt.Sprintf("%d/%s", pp.Port, pp.Proto)
}
