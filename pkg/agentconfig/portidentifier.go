package agentconfig

import (
	"fmt"
	"strconv"
	"strings"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// PortIdentifier identifies a port (service or container) unambiguously using
// the notation <name or number>/<protocol>. A named port will always be identified
// using the name and the protocol will only be appended when it is not TCP.
type PortIdentifier string

// ValidatePort validates a port string. An error is returned if the string isn't a
// number between 1 and 65535 or a DNS_LABEL.
func ValidatePort(s string) error {
	_, err := ParseNumericPort(s)
	if err == ErrNotInteger {
		err = nil
		if errs := validation.IsDNS1035Label(s); len(errs) > 0 {
			err = fmt.Errorf(strings.Join(errs, " and "))
		}
	}
	return err
}

// NewPortIdentifier creates a new PortIdentifier from a protocol and a string that
// is either a name or a number. An error is returned if the protocol is unsupported,
// if a port number is not between 1 and 65535, or if the name isn't a DNS_LABEL.
func NewPortIdentifier(protocol string, portString string) (PortIdentifier, error) {
	if err := ValidatePort(portString); err != nil {
		return "", err
	}
	if protocol != "" {
		pr, err := ParseProtocol(protocol)
		if err != nil {
			return "", err
		}
		portString += string([]byte{ProtoSeparator}) + string(pr)
	}
	return PortIdentifier(portString), nil
}

// HasProto returns the protocol, and the name or number.
func (spi PortIdentifier) HasProto() bool {
	return strings.IndexByte(string(spi), ProtoSeparator) > 0
}

// ProtoAndNameOrNumber returns the protocol, and the name or number.
func (spi PortIdentifier) ProtoAndNameOrNumber() (core.Protocol, string, uint16) {
	s := string(spi)
	p := core.ProtocolTCP
	if ix := strings.IndexByte(s, ProtoSeparator); ix > 0 {
		p = core.Protocol(s[ix+1:])
		s = s[0:ix]
	}
	if n, err := strconv.Atoi(s); err == nil {
		return p, "", uint16(n)
	}
	return p, s, 0
}

func (spi PortIdentifier) String() string {
	return string(spi)
}
