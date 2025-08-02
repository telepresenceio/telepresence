package types

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
)

// PortIdentifier identifies a port (service or container) unambiguously using
// the notation <name or number>/<protocol>. A named port will always be identified
// using the name, and the protocol will only be appended when it is not TCP.
type PortIdentifier string

// ValidatePort validates a port string. An error is returned if the string isn't a
// number between 1 and 65535 or a DNS_LABEL.
func ValidatePort(s string) error {
	_, err := ParsePort(s)
	if err == ErrNotInteger {
		err = nil
		if errs := validation.IsDNS1035Label(s); len(errs) > 0 {
			err = errors.New(strings.Join(errs, " and "))
		}
	}
	return err
}

// NewPortIdentifier creates a new PortIdentifier from a protocol and a string that
// is either a name or a number. An error is returned if the protocol is unsupported,
// if a port number is not between 1 and 65535, or if the name isn't a DNS_LABEL.
func NewPortIdentifier(proto Proto, portString string) (PortIdentifier, error) {
	if err := ValidatePort(portString); err != nil {
		return "", err
	}
	if proto != ProtoTCP {
		portString += string([]byte{ProtoSeparator}) + proto.String()
	}
	return PortIdentifier(portString), nil
}

// HasProto returns the protocol, and the name or number.
func (spi PortIdentifier) HasProto() bool {
	return strings.IndexByte(string(spi), ProtoSeparator) > 0
}

// Validate checks that the PortIdentifier has a valid protocol, and a valid name or number.
func (spi PortIdentifier) Validate() (err error) {
	s := string(spi)
	if ix := strings.IndexByte(s, ProtoSeparator); ix > 0 {
		_, err = ParseProto(s[ix+1:])
		if err != nil {
			return err
		}
		s = s[0:ix]
	}
	return ValidatePort(s)
}

// ProtoAndNameOrNumber returns the protocol, and the name or number.
func (spi PortIdentifier) ProtoAndNameOrNumber() (Proto, string, uint16) {
	s := string(spi)
	p := ProtoTCP
	if ix := strings.IndexByte(s, ProtoSeparator); ix > 0 {
		p, _ = ParseProto(s[ix+1:])
		s = s[0:ix]
	}
	if n, err := ParsePort(s); err == nil {
		return p, "", n
	}
	return p, s, 0
}

func (spi PortIdentifier) AsIntOrStr() intstr.IntOrString {
	_, s, n := spi.ProtoAndNameOrNumber()
	if s == "" {
		return intstr.FromInt32(int32(n))
	}
	return intstr.FromString(s)
}

// String will consistently yield the identifier without the protocol suffix when the protocol is TCP
// and otherwise always use the suffix "/UDP".
func (spi PortIdentifier) String() string {
	p, s, n := spi.ProtoAndNameOrNumber()
	switch {
	case s == "" && p == ProtoTCP:
		return strconv.Itoa(int(n))
	case s != "" && p == ProtoTCP:
		return s
	case s == "":
		return fmt.Sprintf("%d/%s", n, p)
	default:
		return fmt.Sprintf("%s/%s", s, p)
	}
}
