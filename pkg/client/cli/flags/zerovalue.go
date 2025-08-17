package flags

// IsZeroValue returns true if the given string represents a well-known zero value.
func IsZeroValue(str string) bool {
	switch str {
	case "", "false", "<nil>", "[]", "0":
		return true
	}
	return false
}
