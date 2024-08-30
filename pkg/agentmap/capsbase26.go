package agentmap

// CapsBase26 converts the given number into base 26, represented using the letters 'A' to 'Z'.
func CapsBase26(v uint64) string {
	return addBase26('A', v)
}

// Base26 converts the given number into base 26, represented using the letters 'a' to 'z'.
func Base26(v uint64) string {
	return addBase26('a', v)
}

// Base26 converts the given number into base 26 represented using the letters 'a' to 'z'.
func addBase26(c byte, v uint64) string {
	i := 14 // covers v == math.MaxUint64
	b := make([]byte, i)
	for {
		l := v % 26
		i--
		b[i] = c + byte(l)
		if v < 26 {
			break
		}
		v /= 26
	}
	return string(b[i:])
}
