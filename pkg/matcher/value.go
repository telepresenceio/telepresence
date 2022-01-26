package matcher

import (
	"fmt"
	"regexp"
)

// Value comes in two flavors. One that performs an exact match against a string and one that
// uses a regular expression.
type Value interface {
	fmt.Stringer

	// Matches returns true if the given string matches this Value
	Matches(value string) bool

	// Op returns either == or =~.
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

// NewValue returns a Value that is either an exact text headers or a regexp headers. The latter
// is chosen when the given string contains regexp meta characters. An error is returned if the string contains
// meta characters but cannot be compiled into a regexp.
func NewValue(v string) (Value, error) {
	if regexp.QuoteMeta(v) == v {
		return textValue(v), nil
	}
	rx, err := regexp.Compile(v)
	if err != nil {
		return nil, err
	}
	return rxValue{rx}, nil
}
