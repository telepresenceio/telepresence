package extensions

import (
	"encoding/json"
	"math/rand"
	"net"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/spf13/pflag"

	"github.com/datawire/dlib/derror"
)

func TestFlagTypes(t *testing.T) { //nolint:gocognit
	// This is a combo table-driven and fuzzer-drivent test.  For each FlagType, we'll call
	// checkFn with a bunch of different 'a' arguments.  Mostly these will be random fuzzing
	// values from "testing/quick", but we'll also throw in our own table of worthwhile
	// testcases.
	checkFn := func(t *testing.T, flagType TypeEnum, a interface{}) (ret bool) {
		defer func() {
			if err := derror.PanicToError(recover()); err != nil {
				t.Logf("%+v", err)
				ret = false
			}
			t.Logf(" => %v", ret)
		}()

		_, argTyp := flagType.sanityCheck()
		bPtrVal := reflect.New(argTyp)

		// Check that TypeEnum.NewFlagValue works.
		aVal, err := flagType.NewFlagValue(a)
		if err != nil {
			t.Logf("flagType.NewFlagValue(a) returned err: %v", err)
			return false
		}

		// Manually instantiate a Value, such that we can inspect its value via bPtrVal.
		bVal := reflect.ValueOf(flagTypes[flagType]).Call([]reflect.Value{
			reflect.ValueOf(pflag.NewFlagSet("", 0)),
			bPtrVal,
		})[0].Interface().(Value)
		// Make sure extensions can get by using native pflag.
		if pset, ok := bVal.(interface{ UsePflagSet(bool) }); ok {
			pset.UsePflagSet(true)
		}

		// Round-trip from a to b.
		fs := pflag.NewFlagSet("", pflag.ContinueOnError)
		fs.Var(bVal, "tst", "")
		args := aVal.AsArgs("tst")
		t.Logf("args: %#v", args)
		if err := fs.Parse(args); err != nil {
			t.Logf("fs.Parse returned err: %v", err)
			return false
		}

		// Check that round-tripping set the value correctly.
		b := bPtrVal.Elem().Interface()
		if reflect.TypeOf(a) != reflect.TypeOf(b) {
			aPtr, err := convertNew(a, argTyp)
			if err != nil {
				t.Logf("convertNew returned err: %v", err)
				return false
			}
			a = aPtr.Elem().Interface()
		}
		if diff := cmp.Diff(a, b, cmpopts.EquateEmpty()); diff != "" {
			t.Logf("-expected, +actual :\n: %s", diff)
			return false
		}

		return true
	}

	// Our table of worthwhile testcases, plus any special instructions for the fuzzer.  In
	// addition to the testcases listed here, for every flagtype we also always check
	// json.RawMessage(nil) and json.RawMessage(`null`).
	configs := map[TypeEnum]struct {
		Table  []interface{}
		Fuzzer func([]reflect.Value, *rand.Rand)
	}{
		"string-slice": {
			Table: []interface{}{
				[]string{""},
				[]string{"foo,bar", "baz"},
			},
		},
		"string-to-int": {
			// XXX: pflag doesn't support keys with `,` or `=` in them.
			Table: []interface{}{
				map[string]int{
					// "foo,bar": 12,
					// "foo=bar": 13,
					"": 14,
				},
			},
			Fuzzer: func(out []reflect.Value, rand *rand.Rand) {
				var ok bool
				out[0], ok = quick.Value(reflect.TypeOf(map[string]int(nil)), rand)
				if !ok {
					panic("quick.Value failed")
				}
				m := out[0].Interface().(map[string]int)
				for k := range m {
					if strings.Contains(k, ",") || strings.Contains(k, "=") {
						newk := k
						newk = strings.ReplaceAll(newk, ",", "")
						newk = strings.ReplaceAll(newk, "=", "")
						m[newk] = m[k]
						delete(m, k)
					}
				}
			},
		},
		"string-to-int64": {
			// XXX: pflag doesn't support keys with `,` or `=` in them.
			Table: []interface{}{
				map[string]int64{
					// "foo,bar": 12,
					// "foo,bar": 13,
					"": 14,
				},
			},
			Fuzzer: func(out []reflect.Value, rand *rand.Rand) {
				var ok bool
				out[0], ok = quick.Value(reflect.TypeOf(map[string]int64(nil)), rand)
				if !ok {
					panic("quick.Value failed")
				}
				m := out[0].Interface().(map[string]int64)
				for k := range m {
					if strings.Contains(k, ",") || strings.Contains(k, "=") {
						newk := k
						newk = strings.ReplaceAll(newk, ",", "")
						newk = strings.ReplaceAll(newk, "=", "")
						m[newk] = m[k]
						delete(m, k)
					}
				}
			},
		},
		"string-to-string": {
			// XXX: pflag doesn't support keys with `=` in them.
			Table: []interface{}{
				map[string]string{
					// "foo=bar": "baz",
					"foo,bar": "baz",
					"frob":    "blarg,qux",
					"":        "yo",
					"glorp":   `q"`,
					`"q"`:     "",
					`"bala`:   `nced"`,
				},
				// map[string]string{"foo=bar": "baz"},
				map[string]string{"foo,bar": "baz"},
				map[string]string{"frob": "blarg,qux"},
				map[string]string{"": "yo"},
				map[string]string{"glorp": `q"`},
				map[string]string{`"q"`: ""},
				map[string]string{`"bala`: `nced"`},
			},
			Fuzzer: func(out []reflect.Value, rand *rand.Rand) {
				var ok bool
				out[0], ok = quick.Value(reflect.TypeOf(map[string]string(nil)), rand)
				if !ok {
					panic("quick.Value failed")
				}
				m := out[0].Interface().(map[string]string)
				for k := range m {
					if strings.Contains(k, "=") {
						newk := k
						newk = strings.ReplaceAll(newk, "=", "")
						m[newk] = m[k]
						delete(m, k)
					}
				}
			},
		},
		"ip": {
			Fuzzer: func(out []reflect.Value, rand *rand.Rand) {
				out[0] = reflect.ValueOf(randomIP(rand))
			},
		},
		"ip-slice": {
			Fuzzer: func(out []reflect.Value, rand *rand.Rand) {
				ret := make([]net.IP, rand.Intn(50))
				for i := range ret {
					ret[i] = randomIP(rand)
				}
				out[0] = reflect.ValueOf(ret)
			},
		},
		"ipmask": {
			Fuzzer: func(out []reflect.Value, rand *rand.Rand) {
				bs := make([]byte, 4) // XXX pflag only supports IPv4 masks
				rand.Read(bs)
				out[0] = reflect.ValueOf(net.IPMask(bs))
			},
		},
		"ipnet": {
			Fuzzer: func(out []reflect.Value, rand *rand.Rand) {
				ip := randomIP(rand)
				mask := net.CIDRMask(rand.Intn(len(ip)*8), len(ip)*8)
				for i := range ip {
					ip[i] &= mask[i]
				}
				out[0] = reflect.ValueOf(net.IPNet{
					IP:   ip,
					Mask: mask,
				})
			},
		},
	}

	for flagType := range flagTypes {
		flagType := flagType // capture loop variable
		t.Run(string(flagType), func(t *testing.T) {
			// First up, do the basic internal sanity check.  This also gives us info we
			// need to configure the fuzzer.
			var argTyp reflect.Type
			func() {
				defer func() {
					if err := derror.PanicToError(recover()); err != nil {
						t.Fatalf("%+v", err)
					}
				}()
				_, argTyp = flagType.sanityCheck()
			}()

			// Second, go through the table of fixed testcases.
			tableValues := append([]interface{}{
				json.RawMessage(nil),
				json.RawMessage(`null`),
			}, configs[flagType].Table...)
			for i, v := range tableValues {
				if !checkFn(t, flagType, v) {
					t.Errorf("table#%d: failed on input %#v", i-2, v)
				}
			}

			// Third, run the fuzzer.
			wrappedCheckFn := reflect.MakeFunc(
				// signature
				reflect.FuncOf(
					[]reflect.Type{argTyp},               // arguments
					[]reflect.Type{reflect.TypeOf(true)}, // return value
					false,                                // variadic
				),
				// implementation
				func(args []reflect.Value) []reflect.Value {
					if len(args) != 1 {
						panic("got the wrong number of args from quick.Check")
					}

					ret := checkFn(t, flagType, args[0].Interface())
					return []reflect.Value{reflect.ValueOf(ret)}
				},
			).Interface()
			var config *quick.Config
			if values := configs[flagType].Fuzzer; values != nil {
				config = &quick.Config{
					Values: values,
				}
			}
			if err := quick.Check(wrappedCheckFn, config); err != nil {
				t.Error(err)
			}
		})
	}
}

func randomIP(rand *rand.Rand) net.IP {
	size := 4
	if rand.Int()%2 == 0 {
		size = 16
	}
	bs := make([]byte, size)
	rand.Read(bs)
	return net.IP(bs)
}
