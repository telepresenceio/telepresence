package helm

import (
	"reflect"
	"sort"
)

//nolint:gochecknoglobals // the package path of every struct type defined in this file
var pkgPath = reflect.TypeOf(Values{}).PkgPath()

// MergeValues layers over onto base, field by field: a set leaf in over wins,
// a struct field whose own fields are all exported recurses field by field,
// and a struct with an unexported field, a slice, or a map replaces whole.
// Neither input is mutated; the result shares no memory with either.
func MergeValues(over, base *Values) *Values {
	var out Values
	reflect.ValueOf(&out).Elem().Set(mergeStruct(reflect.ValueOf(*over), reflect.ValueOf(*base)))
	return &out
}

// IsZero reports whether v sets no value at all.
func (v *Values) IsZero() bool {
	return reflect.ValueOf(*v).IsZero()
}

// DeepCopy returns a copy of v that shares no memory with it.
func (v *Values) DeepCopy() *Values {
	var out Values
	reflect.ValueOf(&out).Elem().Set(deepClone(reflect.ValueOf(*v)))
	return &out
}

// DiffValues returns the sorted, dotted chart-key paths of every leaf value a
// sets that b lacks or that differs from b's.
func DiffValues(a, b *Values) []string {
	var paths []string
	diffStruct("", reflect.ValueOf(*a), reflect.ValueOf(*b), &paths)
	sort.Strings(paths)
	return paths
}

func mergeStruct(ov, bv reflect.Value) reflect.Value {
	t := ov.Type()
	out := reflect.New(t).Elem()
	for i := range t.NumField() {
		out.Field(i).Set(mergeField(ov.Field(i), bv.Field(i)))
	}
	return out
}

// mergeField returns the overlay field when it is set, otherwise the base
// field; a mergeable struct is merged field by field instead. A zero value
// of any kind counts as unset, so a plain scalar in a Kubernetes API struct
// never overrides the base with its zero.
func mergeField(of, bf reflect.Value) reflect.Value {
	if of.Kind() == reflect.Struct && isMergeableStruct(of.Type()) {
		return mergeStruct(of, bf)
	}
	if isZero(of) {
		return deepClone(bf)
	}
	return deepClone(of)
}

func diffStruct(path string, av, bv reflect.Value, out *[]string) {
	t := av.Type()
	for i := range t.NumField() {
		af, bf := av.Field(i), bv.Field(i)
		fp := jsonName(t.Field(i))
		if path != "" {
			fp = path + "." + fp
		}
		if af.Kind() == reflect.Struct && isMergeableStruct(af.Type()) {
			diffStruct(fp, af, bf, out)
			continue
		}
		if isZero(af) {
			continue
		}
		if isZero(bf) || !reflect.DeepEqual(af.Interface(), bf.Interface()) {
			*out = append(*out, fp)
		}
	}
}

// isMergeableStruct reports whether t is a struct whose fields are all
// exported, the shape that merges and diffs field by field rather than as
// one leaf value.
func isMergeableStruct(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && allFieldsExported(t)
}

func isZero(v reflect.Value) bool {
	switch v.Kind() { //nolint:exhaustive // only the shapes Values ever uses
	case reflect.Pointer, reflect.Slice, reflect.Map:
		return v.IsNil()
	default:
		return v.IsZero()
	}
}

// deepClone returns a copy of v that shares no memory with it, recursing through
// pointers, slices and maps, and through structs whose fields are all exported.
func deepClone(v reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}
	switch v.Kind() { //nolint:exhaustive // only the shapes Values ever uses
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(deepClone(v.Elem()))
		return out
	case reflect.Struct:
		if !allFieldsExported(v.Type()) {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		for i := range v.NumField() {
			out.Field(i).Set(deepClone(v.Field(i)))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(deepClone(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), deepClone(iter.Value()))
		}
		return out
	default:
		return v
	}
}

func allFieldsExported(t reflect.Type) bool {
	for i := range t.NumField() {
		if t.Field(i).PkgPath != "" {
			return false
		}
	}
	return true
}
