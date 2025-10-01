package matcher

import (
	"fmt"
	"regexp"
	"strings"
)

type ValueOp string

const (
	ValueOpEqual  ValueOp = "=="
	ValueOpRegex  ValueOp = "=~"
	ValueOpPrefix ValueOp = "prefix"
)

// Value comes in three flavors. One that performs an exact match against a string, one that
// uses a regular expression, and one that uses prefix matching.
type Value interface {
	fmt.Stringer

	// Matches returns true if the given string matches this Value
	Matches(value string) bool

	// Op returns either ==, =~, or prefix
	Op() ValueOp
}

type textValue string

func (t textValue) Matches(value string) bool {
	return string(t) == value
}

func (t textValue) String() string {
	return string(t)
}

func (t textValue) Op() ValueOp {
	return ValueOpEqual
}

type rxValue struct {
	*regexp.Regexp
}

func (r rxValue) Matches(value string) bool {
	return value != "" && r.MatchString(value)
}

func (r rxValue) Op() ValueOp {
	return ValueOpRegex
}

type prefixValue string

func (p prefixValue) Matches(value string) bool {
	return strings.HasPrefix(value, string(p))
}

func (p prefixValue) String() string {
	return string(p)
}

func (p prefixValue) Op() ValueOp {
	return ValueOpPrefix
}

// NewValue returns a Value that is either an exact or a regexp matcher. The latter is chosen
// when the given string contains regexp meta-characters.
func NewValue(v string) Value {
	if regexp.QuoteMeta(v) == v {
		return NewEqual(v)
	}
	return NewRegex(v)
}

// NewOpValue returns a Value that is a matcher of the given type. If the given type is not
// supported, an exact matcher is returned.
func NewOpValue(op ValueOp, v string) Value {
	switch op {
	case ValueOpRegex:
		return NewRegex(v)
	case ValueOpPrefix:
		return NewPrefix(v)
	default:
		return NewEqual(v)
	}
}

// NewRegex returns a Value that is a regexp matcher. Returns an exact value if the string cannot be
// compiled into a regexp.
func NewRegex(v string) Value {
	rx, err := regexp.Compile(v)
	if err != nil {
		// Treat an invalid regexp as an exact match
		return NewEqual(v)
	}
	return rxValue{rx}
}

// NewPrefix returns a Value that is a prefix matcher.
func NewPrefix(v string) Value {
	return prefixValue(v)
}

// NewEqual returns a Value that is an equal matcher.
func NewEqual(v string) Value {
	return textValue(v)
}
