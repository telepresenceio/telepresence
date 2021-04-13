package subnet

import "fmt"

// ByteSet represents 0 - 255 unique bytes.
type ByteSet [4]uint64

// Add adds a byte to this ByteSet
func (b *ByteSet) Add(bv byte) {
	b[bv>>6] |= uint64(1) << uint64(bv&0x3f)
}

// Remove removes a byte from this ByteSet
func (b *ByteSet) Remove(bv byte) {
	b[bv>>6] &^= uint64(1) << uint64(bv&0x3f)
}

// Contains returns true if this ByteSet contains the given byte
func (b *ByteSet) Contains(bv byte) bool {
	return b[bv>>6]&(uint64(1)<<uint64(bv&0x3f)) != 0
}

// Equals returns true if this ByteSet equals the argument
func (b *ByteSet) Equals(other *ByteSet) bool {
	if other == nil {
		return false
	}
	return *b == *other
}

// ToSlice returns the number of bytes in this ByteSet
func (b *ByteSet) Len() (l int) {
	for _, g := range b {
		if g != 0 {
			for bit := 0; bit < 64; bit++ {
				if g&(uint64(1)<<bit) != 0 {
					l++
				}
			}
		}
	}
	return
}

// String prints the hexadecimal representation of the bits
func (b *ByteSet) String() string {
	return fmt.Sprintf("%0.16x%0.16x%0.16x%0.16x", b[0], b[1], b[2], b[3])
}

// ToSlice returns an ordered slice of all bytes in this ByteSet
func (b *ByteSet) ToSlice() []byte {
	l := b.Len() // faster and more accurate than repeatedly growing a slice
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
// value for all bytes represented by this ByteSet and a byte containing
// the value of those bits.
func (b *ByteSet) Mask() (ones int, value byte) {
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
