package slice

// Contains returns true if the given slice contains the given element
func Contains[E comparable](vs []E, e E) bool {
	for _, v := range vs {
		if e == v {
			return true
		}
	}
	return false
}

// ContainsAll returns true if the first slice contains all elements in the second slice
func ContainsAll[E comparable](vs []E, es []E) bool {
	for _, e := range es {
		if !Contains(vs, e) {
			return false
		}
	}
	return true
}

// ContainsAny returns true if the first slice contains at least one of the elements in the second slice
func ContainsAny[E comparable](vs []E, es []E) bool {
	for _, e := range es {
		if Contains(vs, e) {
			return true
		}
	}
	return false
}
