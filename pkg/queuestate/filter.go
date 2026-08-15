package queuestate

// Overlaps reports whether two equality-conjunction filters can match the
// same message: every key they share must map to the same value in both. Two
// filters that share no key overlap vacuously, and an empty filter matches
// every message, so it overlaps any filter, including another empty one.
// Overlaps is symmetric: Overlaps(a, b) == Overlaps(b, a).
func Overlaps(a, b map[string]string) bool {
	for k, va := range a {
		if vb, ok := b[k]; ok && va != vb {
			return false
		}
	}
	return true
}
