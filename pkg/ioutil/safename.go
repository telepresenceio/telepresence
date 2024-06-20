package ioutil

import "strings"

// SafeName returns a string that can safely be used as a file name or docker container. Only
// characters [a-zA-Z0-9][a-zA-Z0-9_.-] are allowed. Others are replaced by an underscore, or
// if it's the very first character, by the character 'a'.
func SafeName(name string) string {
	n := strings.Builder{}
	for i, c := range name {
		switch {
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			n.WriteByte(byte(c))
		case i > 0 && (c == '_' || c == '.' || c == '-'):
			n.WriteByte(byte(c))
		case i > 0:
			n.WriteByte('_')
		default:
			n.WriteByte('a')
		}
	}
	return n.String()
}
