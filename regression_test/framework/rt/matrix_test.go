package rt

import (
	"reflect"
	"testing"
)

// fourAxisExample is the 3-4 axis example matrix_test.go's property tests
// run against: four axes of varying cardinality (2, 3, 2, 2 values), enough
// to exercise every-two-axes coverage without the pair count getting large.
func fourAxisExample() []Axis {
	return []Axis{
		{Name: "a", Values: []string{"1", "2"}},
		{Name: "b", Values: []string{"x", "y", "z"}},
		{Name: "c", Values: []string{"true", "false"}},
		{Name: "d", Values: []string{"p", "q"}},
	}
}

// assertFullPairCoverage fails t unless every pair of values across every
// two distinct axes appears together in at least one combo.
func assertFullPairCoverage(t *testing.T, axes []Axis, combos []map[string]string) {
	t.Helper()
	for i := 0; i < len(axes); i++ {
		for j := i + 1; j < len(axes); j++ {
			ai, aj := axes[i], axes[j]
			for _, vi := range ai.Values {
				for _, vj := range aj.Values {
					if !anyComboHas(combos, ai.Name, vi, aj.Name, vj) {
						t.Errorf("uncovered pair: %s=%s, %s=%s", ai.Name, vi, aj.Name, vj)
					}
				}
			}
		}
	}
}

func anyComboHas(combos []map[string]string, k1, v1, k2, v2 string) bool {
	for _, c := range combos {
		if c[k1] == v1 && c[k2] == v2 {
			return true
		}
	}
	return false
}

func TestPairwiseCoversAllPairs(t *testing.T) {
	axes := fourAxisExample()
	combos := Pairwise(axes, nil)
	if len(combos) == 0 {
		t.Fatal("Pairwise returned no combinations")
	}
	assertFullPairCoverage(t, axes, combos)

	full := 1
	for _, a := range axes {
		full *= len(a.Values)
	}
	if len(combos) >= full {
		t.Errorf("pairwise combos (%d) did not reduce below the full product (%d)", len(combos), full)
	}
}

func TestPairwiseDeterministic(t *testing.T) {
	axes := fourAxisExample()
	first := Pairwise(axes, nil)
	second := Pairwise(fourAxisExample(), nil) // fresh axes slice/maps, same content
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Pairwise is not deterministic:\nfirst:  %v\nsecond: %v", first, second)
	}
}

func TestPairwiseEveryComboComplete(t *testing.T) {
	axes := fourAxisExample()
	for _, c := range Pairwise(axes, nil) {
		if len(c) != len(axes) {
			t.Fatalf("combo %v has %d keys, want %d", c, len(c), len(axes))
		}
		for _, a := range axes {
			if _, ok := c[a.Name]; !ok {
				t.Fatalf("combo %v missing axis %q", c, a.Name)
			}
		}
	}
}

func TestPairwiseExcludeIsRespected(t *testing.T) {
	axes := fourAxisExample()
	exclude := func(c map[string]string) bool {
		return c["a"] == "1" && c["b"] == "x"
	}
	combos := Pairwise(axes, exclude)
	if anyComboHas(combos, "a", "1", "b", "x") {
		t.Fatal("Pairwise returned an excluded combination")
	}
	// Every other pair, including ones involving a=1 and b=x individually
	// (just not together), must still be covered.
	for i := 0; i < len(axes); i++ {
		for j := i + 1; j < len(axes); j++ {
			ai, aj := axes[i], axes[j]
			for _, vi := range ai.Values {
				for _, vj := range aj.Values {
					if ai.Name == "a" && vi == "1" && aj.Name == "b" && vj == "x" {
						continue // the excluded pair itself is expected to be missing
					}
					if !anyComboHas(combos, ai.Name, vi, aj.Name, vj) {
						t.Errorf("uncovered (non-excluded) pair: %s=%s, %s=%s", ai.Name, vi, aj.Name, vj)
					}
				}
			}
		}
	}
}

func TestPairwiseSingleAxis(t *testing.T) {
	axes := []Axis{{Name: "only", Values: []string{"a", "b", "c"}}}
	combos := Pairwise(axes, nil)
	if len(combos) != 3 {
		t.Fatalf("got %d combos, want 3: %v", len(combos), combos)
	}
	seen := map[string]bool{}
	for _, c := range combos {
		seen[c["only"]] = true
	}
	for _, v := range axes[0].Values {
		if !seen[v] {
			t.Errorf("value %q missing from single-axis combos", v)
		}
	}
}

func TestPairwiseEmptyAxes(t *testing.T) {
	if combos := Pairwise(nil, nil); combos != nil {
		t.Fatalf("expected nil for no axes, got %v", combos)
	}
}

func TestComboName(t *testing.T) {
	combo := map[string]string{"b": "2", "a": "1", "c": "x"}
	if got, want := ComboName(combo), "a=1,b=2,c=x"; got != want {
		t.Fatalf("ComboName(%v) = %q, want %q", combo, got, want)
	}
}

func TestPairwiseTwoAxes(t *testing.T) {
	axes := []Axis{
		{Name: "port", Values: []string{"bare", "local:number", "local:name"}},
		{Name: "verb", Values: []string{"intercept", "replace"}},
	}
	combos := Pairwise(axes, nil)
	assertFullPairCoverage(t, axes, combos)
	// Two axes: full coverage requires exactly max(len) rows at minimum, and
	// the greedy algorithm should hit that lower bound here (3 port values,
	// 2 verb values -> 3 rows suffice by repeating verb values).
	if len(combos) < 3 {
		t.Fatalf("got %d combos, want at least 3", len(combos))
	}
}
