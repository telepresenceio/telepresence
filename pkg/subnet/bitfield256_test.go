package subnet

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestByteSet_Add(t *testing.T) {
	x := *fullSet
	x.SetBit(1)
	assert.True(t, x.Equals(fullSet))

	x = *emptySet
	x.SetBit(1)
	assert.False(t, x.Equals(emptySet))
	assert.True(t, x.GetBit(1))
	assert.False(t, x.GetBit(233))
	x.SetBit(233)
	assert.True(t, x.GetBit(233))
	assert.Equal(t, 2, x.OnesCount())
}

func TestByteSet_Remove(t *testing.T) {
	x := *fullSet
	x.ClearBit(255)
	assert.Equal(t, 255, x.OnesCount())
	x.ClearBit(0)
	assert.Equal(t, 254, x.OnesCount())
}

func TestByteSet_Mask(t *testing.T) {
	bytes00To0F := &Bitfield256{}
	for i := 0; i < 0xf; i++ {
		bytes00To0F.SetBit(byte(i))
	}
	bytesF0ToFF := &Bitfield256{}
	for i := 0xf0; i < 0xff; i++ {
		bytesF0ToFF.SetBit(byte(i))
	}
	tests := []struct {
		name      string
		set       *Bitfield256
		wantOnes  int
		wantValue byte
	}{
		{
			"full set",
			fullSet,
			0,
			0,
		},
		{
			"empty set",
			emptySet,
			8,
			0,
		},
		{
			"00 to 0f",
			bytes00To0F,
			4,
			0,
		},
		{
			"f0 to ff",
			bytesF0ToFF,
			4,
			0xf0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			gotOnes, gotValue := tt.set.Mask()
			if gotOnes != tt.wantOnes {
				t.Errorf("Mask() gotOnes = %v, want %v", gotOnes, tt.wantOnes)
			}
			if gotValue != tt.wantValue {
				t.Errorf("Mask() gotValue = %v, want %v", gotValue, tt.wantValue)
			}
		})
	}
}

var (
	emptySet = &Bitfield256{}
	fullSet  = &Bitfield256{0xffffffffffffffff, 0xffffffffffffffff, 0xffffffffffffffff, 0xffffffffffffffff}
)

func TestByteSet_String(t *testing.T) {
	tests := []struct {
		name string
		set  *Bitfield256
		want string
	}{
		{
			"full set",
			fullSet,
			"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		},
		{
			"empty set",
			emptySet,
			"0000000000000000000000000000000000000000000000000000000000000000",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.set.String(); got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestByteSet_ToSlice(t *testing.T) {
	s := emptySet.ToSlice()
	assert.Equal(t, 0, len(s))
	s = fullSet.ToSlice()
	assert.Equal(t, 256, len(s))
	for i := 0; i < 256; i++ {
		assert.Equal(t, byte(i), s[i])
	}
}
