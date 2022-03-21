package agentconfig

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCapsBase26(t *testing.T) {
	tests := []struct {
		name string
		v    uint64
		want string
	}{
		{
			"zero",
			0,
			"A",
		},
		{
			"25",
			25,
			"Z",
		},
		{
			"26",
			26,
			"BA",
		},
		{
			"51",
			26 + 25,
			"BZ",
		},
		{
			"52",
			2 * 26,
			"CA",
		},
		{
			"1351",
			2*26*26 - 1,
			"BZZ",
		},
		{
			"1352",
			2 * 26 * 26,
			"CAA",
		},
		{
			"maxuint",
			math.MaxUint64,
			"HLHXCZMXSYUMQP",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, CapsBase26(tt.v), "CapsBase26(%v)", tt.v)
		})
	}
}
