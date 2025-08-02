package types

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

type AddrPortProto struct {
	netip.AddrPort
	Proto Proto
}

func ParseAddrPortProto(pm string) (pap AddrPortProto, err error) {
	// Parse backwards. An IPv6 address contains colons.
	pap.Proto = ProtoTCP
	if ix := strings.LastIndexByte(pm, ProtoSeparator); ix > 0 {
		pap.Proto, err = ParseProto(pm[ix+1:])
		if err != nil {
			return pap, err
		}
		pm = pm[:ix]
	}
	pap.AddrPort, err = netip.ParseAddrPort(pm)
	return pap, err
}

func (p AddrPortProto) String() string {
	if p.Proto == ProtoTCP {
		return p.AddrPort.String()
	}
	return fmt.Sprintf("%s/%s", p.AddrPort, p.Proto)
}

func (p AddrPortProto) MarshalBinary() ([]byte, error) {
	bs, err := p.AddrPort.MarshalBinary()
	if err == nil {
		bs = append(bs, byte(p.Proto))
	}
	return bs, err
}

func (p AddrPortProto) MarshalJSONTo(out *jsontext.Encoder) error {
	return json.MarshalEncode(out, p.String())
}

func (p *AddrPortProto) UnmarshalBinary(bs []byte) error {
	last := len(bs) - 1
	if last < 2 {
		return errors.New("unexpected slice size")
	}
	err := p.AddrPort.UnmarshalBinary(bs[:last])
	if err == nil {
		p.Proto = Proto(bs[last])
	}
	return err
}

func (p *AddrPortProto) UnmarshalJSONFrom(in *jsontext.Decoder) error {
	var s string
	err := json.UnmarshalDecode(in, &s)
	if err == nil {
		*p, err = ParseAddrPortProto(s)
	}
	return err
}
