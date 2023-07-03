package state

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

// Hack around issues in testify
// https://github.com/stretchr/testify/issues/930
// https://github.com/stretchr/testify/issues/758#issuecomment-645282184
type Assertions struct {
	*assert.Assertions
	t *testing.T
}

func (a *Assertions) Equal(expected any, actual any, msgAndArgs ...any) bool {
	a.t.Helper()
	_, expectedIsProto := expected.(proto.Message)
	_, actualIsProto := actual.(proto.Message)
	if expectedIsProto && actualIsProto {
		if diff := cmp.Diff(expected, actual, cmp.Comparer(proto.Equal)); diff != "" {
			return a.Assertions.Fail("Not equal (-expected +actual):\n"+diff, msgAndArgs...)
		}

		return true
	}
	return a.Assertions.Equal(expected, actual, msgAndArgs...)
}

func (a *Assertions) Contains(s, contains any, msgAndArgs ...any) bool {
	a.t.Helper()
	if contains, isProto := contains.(proto.Message); isProto {
		ok, found := includeElement(s, contains)
		if !ok {
			return a.Assertions.Fail(fmt.Sprintf("%#v could not be applied builtin len()", s), msgAndArgs...)
		}
		if !found {
			return a.Assertions.Fail(fmt.Sprintf("%#v does not contain %#v", s, contains), msgAndArgs...)
		}
		return true
	}
	return a.Assertions.Contains(s, contains, msgAndArgs...)
}

// containsElement try loop over the list check if the list includes the element.
// return (false, false) if impossible.
// return (true, false) if element was not found.
// return (true, true) if element was found.
//
// This is a copy of testify's assert.includeElement, but is modified to use proto.equal instead of
// assert.ObjectsAreEqual, and also modified to use map values instead of map keys.
func includeElement(list any, element proto.Message) (ok, found bool) {
	listValue := reflect.ValueOf(list)
	listKind := reflect.TypeOf(list).Kind()
	defer func() {
		if e := recover(); e != nil {
			fmt.Println("ERR", e)
			ok = false
			found = false
		}
	}()

	if listKind == reflect.Map {
		mapKeys := listValue.MapKeys()
		for i := 0; i < len(mapKeys); i++ {
			if proto.Equal(listValue.MapIndex(mapKeys[i]).Interface().(proto.Message), element) {
				return true, true
			}
		}
		return true, false
	}

	for i := 0; i < listValue.Len(); i++ {
		if proto.Equal(listValue.Index(i).Interface().(proto.Message), element) {
			return true, true
		}
	}
	return true, false
}

func assertNew(t *testing.T) *Assertions {
	return &Assertions{
		Assertions: assert.New(t),
		t:          t,
	}
}
