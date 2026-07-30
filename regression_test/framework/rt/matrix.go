package rt

import (
	"sort"
	"strings"
)

// Axis is one dimension of a pairwise matrix: a name (used as the combo's
// map key) and the values it can take.
type Axis struct {
	Name   string
	Values []string
}

// Pairwise generates a deterministic, seedless set of combinations covering
// every pair of values across every two axes at least once -- a standard
// greedy all-pairs construction -- skipping any combination for which
// exclude returns true. exclude may be nil.
//
// The generator enumerates the full cross product of axes (small by
// construction: pairwise testing only pays off for a handful of
// low-cardinality axes, which is what every caller in this framework uses
// it for), drops combinations exclude rejects, then greedily picks, at each
// step, the remaining combination that covers the most still-uncovered
// pairs. Ties are broken by cross-product order, so two calls with
// identical inputs return identical output (same combinations, same
// order). A pair that no surviving combination can satisfy (every
// combination containing it was excluded) is simply left uncovered; the
// loop still terminates because it stops as soon as no remaining
// combination covers anything new.
func Pairwise(axes []Axis, exclude func(combo map[string]string) bool) []map[string]string {
	if len(axes) == 0 {
		return nil
	}
	if len(axes) == 1 {
		return pairwiseSingleAxis(axes[0], exclude)
	}

	combos := cartesian(axes)
	if exclude != nil {
		kept := combos[:0]
		for _, c := range combos {
			if !exclude(c) {
				kept = append(kept, c)
			}
		}
		combos = kept
	}
	if len(combos) == 0 {
		return nil
	}

	needed := allPairKeys(axes)
	comboPairs := make([]map[string]struct{}, len(combos))
	for i, c := range combos {
		comboPairs[i] = pairKeysOf(c, axes)
	}

	covered := make(map[string]struct{}, len(needed))
	used := make([]bool, len(combos))
	var result []map[string]string

	for len(covered) < len(needed) {
		best, bestNew := -1, 0
		for i, cp := range comboPairs {
			if used[i] {
				continue
			}
			if n := countNew(cp, covered); n > bestNew {
				best, bestNew = i, n
			}
		}
		if best == -1 {
			// No remaining combination covers a new pair: whatever is left
			// uncovered is unreachable under exclude.
			break
		}
		used[best] = true
		result = append(result, combos[best])
		for k := range comboPairs[best] {
			covered[k] = struct{}{}
		}
	}
	return result
}

// pairwiseSingleAxis is Pairwise's degenerate one-axis case: there is no
// "every two axes" pair to cover, so coverage means every surviving value
// appears in its own combination.
func pairwiseSingleAxis(axis Axis, exclude func(combo map[string]string) bool) []map[string]string {
	var out []map[string]string
	for _, v := range axis.Values {
		c := map[string]string{axis.Name: v}
		if exclude != nil && exclude(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// cartesian returns the full cross product of axes' values, one map per
// combination, keyed by axis name, in odometer order (last axis varies
// fastest).
func cartesian(axes []Axis) []map[string]string {
	total := 1
	for _, a := range axes {
		total *= len(a.Values)
	}
	if total == 0 {
		return nil
	}
	combos := make([]map[string]string, 0, total)
	idx := make([]int, len(axes))
	for {
		c := make(map[string]string, len(axes))
		for i, a := range axes {
			c[a.Name] = a.Values[idx[i]]
		}
		combos = append(combos, c)

		pos := len(axes) - 1
		for pos >= 0 {
			idx[pos]++
			if idx[pos] < len(axes[pos].Values) {
				break
			}
			idx[pos] = 0
			pos--
		}
		if pos < 0 {
			break
		}
	}
	return combos
}

// pairKey canonicalizes one (axis, value)-(axis, value) pair for a pair of
// distinct axes i<j into a map key.
func pairKey(ai, aj Axis, vi, vj string) string {
	return ai.Name + "=" + vi + "\x1f" + aj.Name + "=" + vj
}

// allPairKeys returns every (axis, value)-(axis, value) pair key across
// every two distinct axes, independent of exclude: the theoretical coverage
// target.
func allPairKeys(axes []Axis) map[string]struct{} {
	keys := make(map[string]struct{})
	for i := 0; i < len(axes); i++ {
		for j := i + 1; j < len(axes); j++ {
			for _, vi := range axes[i].Values {
				for _, vj := range axes[j].Values {
					keys[pairKey(axes[i], axes[j], vi, vj)] = struct{}{}
				}
			}
		}
	}
	return keys
}

// pairKeysOf returns every pair key combo satisfies, over every two
// distinct axes.
func pairKeysOf(combo map[string]string, axes []Axis) map[string]struct{} {
	keys := make(map[string]struct{}, len(axes)*(len(axes)-1)/2)
	for i := 0; i < len(axes); i++ {
		for j := i + 1; j < len(axes); j++ {
			keys[pairKey(axes[i], axes[j], combo[axes[i].Name], combo[axes[j].Name])] = struct{}{}
		}
	}
	return keys
}

// countNew returns how many of cp's pair keys are absent from covered.
func countNew(cp map[string]struct{}, covered map[string]struct{}) int {
	n := 0
	for k := range cp {
		if _, ok := covered[k]; !ok {
			n++
		}
	}
	return n
}

// ComboName renders combo as a deterministic subtest name: keys sorted,
// joined "key=value,key=value,...". Both matrix consumers (golden chart
// rendering, live pairwise suites) use it so subtest names are stable
// across runs and safe to pass to `go test -run`.
func ComboName(combo map[string]string) string {
	keys := make([]string, 0, len(combo))
	for k := range combo {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + combo[k]
	}
	return strings.Join(parts, ",")
}
