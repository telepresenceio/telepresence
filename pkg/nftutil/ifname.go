//go:build linux

package nftutil

import (
	"github.com/google/nftables/expr"
)

// ifNameSize is IFNAMSIZ: nft pads interface names to this length when
// comparing them as raw register data.
const ifNameSize = 16

// IfnameBytes pads name to IFNAMSIZ bytes the way nft encodes interface-name
// comparisons.
func IfnameBytes(name string) []byte {
	b := make([]byte, ifNameSize)
	copy(b, name)
	return b
}

// MatchOif returns the expressions for matching the outbound interface name
// (`oifname "name"`).
func MatchOif(name string) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: IfnameBytes(name)},
	}
}
