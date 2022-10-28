package subnet

import (
	"fmt"
	"math/bits"
)

// Bitfield256 represents 0 - 255 unique bytes.
type Bitfield256 [4]uint64

// SetBit sets the 1<<bv bit of the bitfield to 1.
func (b *Bitfield256) SetBit(bv byte) {
	b[bv>>6] |= uint64(1) << uint64(bv&0x3f)
}

// ClearBit clears the 1<<bv bit of the bitfield to 0.
func (b *Bitfield256) ClearBit(bv byte) {
	b[bv>>6] &^= uint64(1) << uint64(bv&0x3f)
}

// GetBit returns the value of the 1<<bv bit of the bitfield (0 is false, 1 is true).
func (b *Bitfield256) GetBit(bv byte) bool {
	return b[bv>>6]&(uint64(1)<<uint64(bv&0x3f)) != 0
}

// Equals returns true if this Bitfield256 equals the argument.
func (b *Bitfield256) Equals(other *Bitfield256) bool {
	if other == nil {
		return false
	}
	return *b == *other
}

// OnesCount returns the number of 1 bits in the bitfield.
func (b *Bitfield256) OnesCount() (l int) {
	for _, g := range b {
		if g != 0 {
			l += bits.OnesCount64(g)
		}
	}
	return
}

// String prints the hexadecimal representation of the bits.
func (b *Bitfield256) String() string {
	return fmt.Sprintf("%0.16x%0.16x%0.16x%0.16x", b[0], b[1], b[2], b[3])
}

// ToSlice returns an ordered slice of all bytes in this Bitfield256.
func (b *Bitfield256) ToSlice() []byte {
	l := b.OnesCount() // faster and more accurate than repeatedly growing a slice
	if l == 0 {
		return []byte{}
	}
	slice := make([]byte, l)
	i := 0
	for bi, g := range b {
		if g != 0 {
			bx := bi << 6
			for bit := 0; bit < 64; bit++ {
				if g&(uint64(1)<<bit) != 0 {
					slice[i] = byte(bx | bit)
					i++
				}
			}
		}
	}
	return slice
}

// Mask returns how many bits, from left to right, that have the same
// value for all bytes represented by this Bitfield256 and a byte containing
// the value of those bits.
func (b *Bitfield256) Mask() (ones int, value byte) {
	for testBit := 7; testBit >= 0; testBit-- {
		hasBit := false
		first := true
		v := 1 << testBit
		for i, g := range b {
			if g != 0 {
				bx := i << 6 // top two bits of bytes in this group
				for bit := 0; bit < 64; bit++ {
					if g&(uint64(1)<<bit) != 0 {
						bv := bx | bit
						if first {
							first = false
							hasBit = bv&v != 0
						} else if hasBit != (bv&v != 0) {
							return 7 - testBit, value
						}
					}
				}
			}
		}
		if hasBit {
			value |= byte(v)
		}
	}
	return 8, value
}
