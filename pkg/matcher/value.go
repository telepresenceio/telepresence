package matcher

import (
	"fmt"
	"regexp"
	"strings"
)

// Value comes in three flavors. One that performs an exact match against a string, one that
// uses a regular expression, and one that uses prefix matching.
type Value interface {
	fmt.Stringer

	// Matches returns true if the given string matches this Value
	Matches(value string) bool

	// Op returns either ==, =~, or prefix
	Op() string
}

type textValue string

func (t textValue) Matches(value string) bool {
	return string(t) == value
}

func (t textValue) String() string {
	return string(t)
}

func (t textValue) Op() string {
	return "=="
}

type rxValue struct {
	*regexp.Regexp
}

func (r rxValue) Matches(value string) bool {
	return value != "" && r.MatchString(value)
}

func (r rxValue) Op() string {
	return "=~"
}

type prefixValue string

func (p prefixValue) Matches(value string) bool {
	return strings.HasPrefix(value, string(p))
}

func (p prefixValue) String() string {
	return string(p)
}

func (p prefixValue) Op() string {
	return "prefix"
}

// NewValue returns a Value that is either an exact or a regexp matcher. The latter is chosen
// when the given string contains regexp meta characters. An error is returned if the string contains
// meta characters but cannot be compiled into a regexp.
func NewValue(v string) (Value, error) {
	if regexp.QuoteMeta(v) == v {
		return NewEqual(v), nil
	}
	return NewRegex(v)
}

// NewRegex returns a Value that is a regexp matcher. An error is returned if the string cannot be
// compiled into a regexp.
func NewRegex(v string) (Value, error) {
	rx, err := regexp.Compile(v)
	if err != nil {
		return nil, err
	}
	return rxValue{rx}, nil
}

// NewPrefix returns a Value that is a prefix matcher.
func NewPrefix(v string) Value {
	return prefixValue(v)
}

// NewEqual returns a Value that is an equal matcher.
func NewEqual(v string) Value {
	return textValue(v)
}
