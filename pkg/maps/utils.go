package maps

import (
	"cmp"
	"slices"
	"sort"
)

// Copy creates a copy of the given map and returns it.
func Copy[K comparable, V any](a map[K]V) map[K]V {
	c := make(map[K]V, len(a))
	for k, v := range a {
		c[k] = v
	}
	return c
}

// Equal returns true if the two maps contain the exact same set of associations.
func Equal[K comparable, V comparable](a, b map[K]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if v != b[k] {
			return false
		}
	}
	return true
}

// Merge merges map src into dst, giving the entries in src higher priority.
func Merge[K comparable, V any](dst, src map[K]V) {
	for k, v := range src {
		dst[k] = v
	}
}

// KeySlice returns a slice containing the keys of the map m.
func KeySlice[M ~map[K]V, K comparable, V any](m M) []K {
	r := make([]K, len(m))
	i := 0
	for k := range m {
		r[i] = k
		i++
	}
	return r
}

// SortedKeys returns the keys of the map m sorted alphabetically.
func SortedKeys[M ~map[K]V, K cmp.Ordered, V any](m M) []K {
	r := KeySlice(m)
	slices.Sort(r)
	return r
}

// ToSortedSlice returns a slice of the values in the given map, sorted by that map's keys.
func ToSortedSlice[K cmp.Ordered, V any](m map[K]V) []V {
	ns := make([]K, len(m))
	i := 0
	for n := range m {
		ns[i] = n
		i++
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i] < ns[j] })
	vs := make([]V, i)
	for i, n := range ns {
		vs[i] = m[n]
	}
	return vs
}

func DeltaUpdate[K comparable, V any](m map[K]V, upserts map[K]V, removals []K) {
	for k, v := range upserts {
		m[k] = v
	}
	for _, k := range removals {
		delete(m, k)
	}
}

func Values[K comparable, V any](m map[K]V) []V {
	ml := len(m)
	vs := make([]V, ml)
	for _, v := range m {
		ml--
		vs[ml] = v
	}
	return vs
}
