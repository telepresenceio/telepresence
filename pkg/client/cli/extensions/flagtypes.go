package extensions

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
)

type Value interface {
	pflag.Value
	AsArgs(flagname string) []string
}

// TypeEnum is an enum-string that identifies the datatype to use both for (1) parsing the default
// value of a flag, and for (2) validating and normalizing the flag value that the user passes on
// the CLI.
//
// See the `flagTypes` variable in `flagtypes.go` for a listing of valid values.
type TypeEnum string

func (t *TypeEnum) UnmarshalJSON(dat []byte) error {
	var str string
	if err := json.Unmarshal(dat, &str); err != nil {
		return err
	}
	if _, ok := flagTypes[TypeEnum(str)]; !ok {
		return fmt.Errorf("invalid flag type: %q", str)
	}
	*t = TypeEnum(str)
	return nil
}

func (t TypeEnum) sanityCheck() (ctorFn reflect.Value, ctorArgType reflect.Type) {
	fnVal := reflect.ValueOf(flagTypes[t])
	if x := fnVal.Kind(); x != reflect.Func {
		panic(fmt.Errorf("flagTypes[%q] is invalid: not a function: %v", string(t), x))
	}
	fnTyp := fnVal.Type()
	if x := fnTyp.NumOut(); x != 1 {
		panic(fmt.Errorf("flagTypes[%q] is invalid: return values %d != 1", string(t), x))
	}
	if x := fnTyp.Out(0); x != reflect.TypeOf((*Value)(nil)).Elem() {
		panic(fmt.Errorf("flagTypes[%q] is invalid: return value: %v", string(t), x))
	}
	if x := fnTyp.NumIn(); x != 2 {
		panic(fmt.Errorf("flagTypes[%q] is invalid: arguments %d != 2", string(t), x))
	}
	if x := fnTyp.In(0); x != reflect.TypeOf((*pfs)(nil)) {
		panic(fmt.Errorf("flagTypes[%q] is invalid: argument[0]: %v", string(t), x))
	}
	if x := fnTyp.In(1).Kind(); x != reflect.Ptr {
		panic(fmt.Errorf("flagTypes[%q] is invalid: argument[1]: %v", string(t), x))
	}
	return fnVal, fnTyp.In(1).Elem()
}

func convertNew(val interface{}, to reflect.Type) (ptr reflect.Value, err error) {
	// convert the default value to the correct type
	bs, err := json.Marshal(val)
	if err != nil {
		return reflect.Value{}, err
	}
	ptr = reflect.New(to)
	if !bytes.Equal(bs, []byte(`null`)) {
		if err := json.Unmarshal(bs, ptr.Interface()); err != nil {
			return reflect.Value{}, err
		}
	}
	return ptr, nil
}

func (t TypeEnum) NewFlagValue(untypedDefault interface{}) (Value, error) {
	// validate the constructor function
	fnVal, defaultTyp := t.sanityCheck()

	// convert the default value to the correct type
	typedDefaultPtrVal, err := convertNew(untypedDefault, defaultTyp)
	if err != nil {
		return nil, fmt.Errorf("invalid default value for type: %w", err)
	}

	// call the constructor function
	retVal := fnVal.Call([]reflect.Value{
		reflect.ValueOf(pflag.NewFlagSet("", 0)),
		typedDefaultPtrVal,
	})[0].Interface().(Value)

	return retVal, nil
}

type pfs = pflag.FlagSet // to make the following table a little less over-verbose

// flagTypes defines the list of valid values for a TypeEnum.
//
// The key of the flagTypes map is the string name to use for the TypeEnum in a .yml file.
//
// The value of the flagTypes map is of type `func(fs *pflag.FlagSet, x T) Value`, where `T` is the
// appropriate type for that flag-type's values.  The function should return a `Value` that is
// initialized with `x` as the value and will adjust `x` when it gets set.  The function may or may
// not use `fs` as a scratch space; it exists only for convenience, the function may do with `fs`
// whatever it likes.
var flagTypes = map[TypeEnum]interface{}{
	// For now, just match pflag (this list is in-sync with v1.0.5).
	// We can define our own additional pflag.Value types later, if we ever feel the need.

	// numbers /////////////////////////////////////////////////////////////

	"int":   func(fs *pfs, x *int) Value { fs.IntVar(x, "x", *x, ""); return simple(fs) },
	"int8":  func(fs *pfs, x *int8) Value { fs.Int8Var(x, "x", *x, ""); return simple(fs) },
	"int16": func(fs *pfs, x *int16) Value { fs.Int16Var(x, "x", *x, ""); return simple(fs) },
	"int32": func(fs *pfs, x *int32) Value { fs.Int32Var(x, "x", *x, ""); return simple(fs) },
	"int64": func(fs *pfs, x *int64) Value { fs.Int64Var(x, "x", *x, ""); return simple(fs) },

	"int-slice":   func(fs *pfs, x *[]int) Value { fs.IntSliceVar(x, "x", *x, ""); return slice(fs, x) },
	"int32-slice": func(fs *pfs, x *[]int32) Value { fs.Int32SliceVar(x, "x", *x, ""); return slice(fs, x) },
	"int64-slice": func(fs *pfs, x *[]int64) Value { fs.Int64SliceVar(x, "x", *x, ""); return slice(fs, x) },
	// no int8-slice or int16-slice :(

	"uint":   func(fs *pfs, x *uint) Value { fs.UintVar(x, "x", *x, ""); return simple(fs) },
	"uint8":  func(fs *pfs, x *uint8) Value { fs.Uint8Var(x, "x", *x, ""); return simple(fs) },
	"uint16": func(fs *pfs, x *uint16) Value { fs.Uint16Var(x, "x", *x, ""); return simple(fs) },
	"uint32": func(fs *pfs, x *uint32) Value { fs.Uint32Var(x, "x", *x, ""); return simple(fs) },
	"uint64": func(fs *pfs, x *uint64) Value { fs.Uint64Var(x, "x", *x, ""); return simple(fs) },

	"uint-slice": func(fs *pfs, x *[]uint) Value { fs.UintSliceVar(x, "x", *x, ""); return slice(fs, x) },
	// no uint{x}-slice :(

	"float32":       func(fs *pfs, x *float32) Value { fs.Float32Var(x, "x", *x, ""); return simple(fs) },
	"float32-slice": func(fs *pfs, x *[]float32) Value { fs.Float32SliceVar(x, "x", *x, ""); return slice(fs, x) },

	"float64":       func(fs *pfs, x *float64) Value { fs.Float64Var(x, "x", *x, ""); return simple(fs) },
	"float64-slice": func(fs *pfs, x *[]float64) Value { fs.Float64SliceVar(x, "x", *x, ""); return slice(fs, x) },

	"count": func(fs *pfs, x *int) Value {
		val := *x
		fs.CountVar(x, "x", "")
		if err := fs.Lookup("x").Value.Set(strconv.Itoa(val)); err != nil {
			// CountVar()'s .Value should always be able to set and int-string.
			panic(err)
		}
		return simple(fs)
	},

	// Booleans ////////////////////////////////////////////////////////////

	"bool":       func(fs *pfs, x *bool) Value { fs.BoolVar(x, "x", *x, ""); return simple(fs) },
	"bool-slice": func(fs *pfs, x *[]bool) Value { fs.BoolSliceVar(x, "x", *x, ""); return slice(fs, x) },

	// durations ///////////////////////////////////////////////////////////

	"duration":       func(fs *pfs, x *time.Duration) Value { fs.DurationVar(x, "x", *x, ""); return simple(fs) },
	"duration-slice": func(fs *pfs, x *[]time.Duration) Value { fs.DurationSliceVar(x, "x", *x, ""); return slice(fs, x) },

	// networking //////////////////////////////////////////////////////////

	"ip":       func(fs *pfs, x *net.IP) Value { fs.IPVar(x, "x", *x, ""); return nilable(fs, x) },
	"ip-slice": func(fs *pfs, x *[]net.IP) Value { fs.IPSliceVar(x, "x", *x, ""); return slice(fs, x) },

	"ipmask": func(fs *pfs, x *net.IPMask) Value { fs.IPMaskVar(x, "x", *x, ""); return nilable(fs, x) },
	// no ipmask-slice :(

	"ipnet": func(fs *pfs, x *net.IPNet) Value {
		fs.IPNetVar(x, "x", *x, "")
		return complexValue{
			Value: fs.Lookup("x").Value,
			ptr:   reflect.ValueOf(x),
			asArgs: func(flagname string, _ reflect.Value) []string {
				if x.IP == nil {
					return []string{}
				}
				return []string{"--" + flagname + "=" + x.String()}
			},
		}
	},
	// no ipnet-slice :(

	// strings /////////////////////////////////////////////////////////////

	"bytes-base64": func(fs *pfs, x *[]byte) Value { fs.BytesBase64Var(x, "x", *x, ""); return simple(fs) },
	"bytes-hex":    func(fs *pfs, x *[]byte) Value { fs.BytesHexVar(x, "x", *x, ""); return simple(fs) },

	"string": func(fs *pfs, x *string) Value { fs.StringVar(x, "x", *x, ""); return simple(fs) },
	"string-slice": func(fs *pfs, x *[]string) Value {
		fs.StringSliceVar(x, "x", *x, "")
		return complexValue{
			Value:  fs.Lookup("x").Value,
			ptr:    reflect.ValueOf(x),
			asArgs: stringSliceAsArgs,
		}
	},
	"string-array": func(fs *pfs, x *[]string) Value { fs.StringArrayVar(x, "x", *x, ""); return slice(fs, x) },

	// maps ////////////////////////////////////////////////////////////////

	"string-to-int":   func(fs *pfs, x *map[string]int) Value { fs.StringToIntVar(x, "x", *x, ""); return mapping(fs, x) },
	"string-to-int64": func(fs *pfs, x *map[string]int64) Value { fs.StringToInt64Var(x, "x", *x, ""); return mapping(fs, x) },
	"string-to-string": func(fs *pfs, x *map[string]string) Value {
		fs.StringToStringVar(x, "x", *x, "")
		return stringMapping(fs, x)
	},
}

type simpleValue struct {
	pflag.Value
	ptr reflect.Value
}

func (v simpleValue) AsArgs(flagname string) []string {
	if (v.ptr != reflect.Value{}) && v.ptr.Elem().IsNil() {
		return []string{}
	}
	return []string{"--" + flagname + "=" + v.String()}
}

func simple(fs *pflag.FlagSet) Value {
	return simpleValue{
		Value: fs.Lookup("x").Value,
	}
}

func nilable(fs *pflag.FlagSet, ptr interface{}) Value {
	return simpleValue{
		Value: fs.Lookup("x").Value,
		ptr:   reflect.ValueOf(ptr),
	}
}

type complexValue struct {
	pflag.Value
	ptr    reflect.Value
	asArgs func(flagname string, ptr reflect.Value) []string
}

func (v complexValue) AsArgs(flagname string) []string {
	return v.asArgs(flagname, v.ptr)
}

func sliceAsArgs(flagname string, slicePtr reflect.Value) []string {
	ret := make([]string, slicePtr.Elem().Len())
	for i := range ret {
		val := slicePtr.Elem().Index(i).Interface()
		if _, ok := val.(fmt.Stringer); ok {
			ret[i] = fmt.Sprintf("--%s=%s", flagname, val)
		} else {
			ret[i] = fmt.Sprintf("--%s=%v", flagname, val)
		}
	}
	return ret
}

func mapAsArgs(flagname string, mapPtr reflect.Value) []string {
	ret := make([]string, 0, mapPtr.Elem().Len())
	iter := mapPtr.Elem().MapRange()
	for iter.Next() {
		key := iter.Key().Interface()
		val := iter.Value().Interface()
		if _, ok := val.(fmt.Stringer); ok {
			ret = append(ret, fmt.Sprintf("--%s=%s=%s", flagname, key, val))
		} else {
			ret = append(ret, fmt.Sprintf("--%s=%s=%v", flagname, key, val))
		}
	}
	return ret
}

func stringSliceAsArgs(flagname string, slicePtr reflect.Value) []string {
	val := slicePtr.Elem().Interface().([]string)
	csv := asCSV(val)
	if len(val) != 0 && csv == "" {
		csv = `""`
	}
	return []string{
		fmt.Sprintf("--%s=%s", flagname, csv),
	}
}

func slice(fs *pflag.FlagSet, slicePtr interface{}) Value {
	return complexValue{
		Value:  fs.Lookup("x").Value,
		ptr:    reflect.ValueOf(slicePtr),
		asArgs: sliceAsArgs,
	}
}

func mapping(fs *pflag.FlagSet, mapPtr interface{}) Value {
	return complexValue{
		Value:  fs.Lookup("x").Value,
		ptr:    reflect.ValueOf(mapPtr),
		asArgs: mapAsArgs,
	}
}

type stringToStringValue struct {
	pflag.Value
	pflagSet bool
	ptr      *map[string]string
}

func (v *stringToStringValue) Set(val string) error {
	if v.pflagSet {
		return v.Value.Set(val)
	}

	// pflag does bad handling when the arg begins or ends with `"` but only contains one KV
	// pair, hack around that.
	if strings.Count(val, "=") == 1 && (strings.HasPrefix(val, `"`) || strings.HasSuffix(val, `"`)) {
		if _, err := csv.NewReader(strings.NewReader(val)).Read(); err != nil {
			val = asCSV([]string{val})
		}
		val += "\n="
	}

	return v.Value.Set(val)
}

func (v *stringToStringValue) UsePflagSet(use bool) {
	v.pflagSet = use
}

func (v *stringToStringValue) AsArgs(flagname string) []string {
	if len(*v.ptr) == 0 {
		return nil
	}
	fields := make([]string, 0, len(*v.ptr))
	for k, v := range *v.ptr {
		fields = append(fields, k+"="+v)
	}
	csv := asCSV(fields)

	// pflag does bad handling when the arg begins or ends with `"` but only contains one KV
	// pair, hack around that.
	if len(fields) == 1 && (strings.HasPrefix(csv, `"`) || strings.HasSuffix(csv, `"`)) {
		csv += "\n="
	}

	return []string{
		fmt.Sprintf("--%s=%s", flagname, csv),
	}
}

func stringMapping(fs *pflag.FlagSet, mapPtr *map[string]string) Value {
	return &stringToStringValue{
		Value: fs.Lookup("x").Value,
		ptr:   mapPtr,
	}
}

func asCSV(vals []string) string {
	b := &bytes.Buffer{}
	w := csv.NewWriter(b)
	if err := w.Write(vals); err != nil {
		// The enderlying bytes.Buffer should never error.
		panic(err)
	}
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}
