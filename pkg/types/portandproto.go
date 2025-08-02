package types

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

var ErrNotInteger = errors.New("not an integer")

const ProtoSeparator = byte('/')

// ParsePort parses the given string into a positive unsigned 16-bit integer.
// ErrNotInteger is returned if the string doesn't represent an integer.
// A range error is return unless the integer is between 1 and 65535.
func ParsePort(portStr string) (uint16, error) {
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, ErrNotInteger
	}
	if port < 1 || port > math.MaxUint16 {
		return 0, fmt.Errorf("%s is not between 1 and 65535", portStr)
	}
	return uint16(port), nil
}

type PortAndProto struct {
	Port  uint16
	Proto Proto
}

func ParsePortAndProto(s string) (PortAndProto, error) {
	pp := PortAndProto{Proto: ProtoTCP}
	var err error
	if ix := strings.IndexByte(s, ProtoSeparator); ix > 0 {
		if pp.Proto, err = ParseProto(s[ix+1:]); err != nil {
			return pp, err
		}
		s = s[0:ix]
	}
	pp.Port, err = ParsePort(s)
	return pp, err
}

func (pp *PortAndProto) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, pp.String())
}

// String will consistently yield the identifier without the protocol suffix when the protocol is TCP
// and otherwise always use the suffix "/UDP".
func (pp *PortAndProto) String() string {
	if pp.Proto == ProtoTCP {
		return strconv.Itoa(int(pp.Port))
	}
	return fmt.Sprintf("%d/%s", pp.Port, pp.Proto)
}

func (pp *PortAndProto) UnmarshalJSONFrom(in *jsontext.Decoder) error {
	var s string
	err := json.UnmarshalDecode(in, &s)
	if err == nil {
		*pp, err = ParsePortAndProto(s)
	}
	return err
}
