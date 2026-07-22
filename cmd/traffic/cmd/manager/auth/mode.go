package auth

import (
	"fmt"
	"strings"
)

// Mode controls how the traffic-manager treats caller authentication.
type Mode string

const (
	// ModeDisabled performs no token validation at all: the interceptor never
	// reads metadata or calls TokenReview, and every call proceeds without a
	// Principal.
	ModeDisabled Mode = "disabled"

	// ModePermissive validates bearer tokens and records the resulting
	// Principal, but never rejects a call on account of authentication.
	ModePermissive Mode = "permissive"

	// ModeEnforcing validates bearer tokens and rejects calls that arrive
	// without one, present an invalid one, or whose claims don't match what
	// they claim to be.
	ModeEnforcing Mode = "enforcing"
)

func (m Mode) String() string {
	return string(m)
}

// UnmarshalText parses a Mode, case-insensitively. An empty value defaults to
// ModePermissive.
func (m *Mode) UnmarshalText(text []byte) error {
	s := string(text)
	if s == "" {
		*m = ModePermissive
		return nil
	}
	switch Mode(strings.ToLower(s)) {
	case ModeDisabled:
		*m = ModeDisabled
	case ModePermissive:
		*m = ModePermissive
	case ModeEnforcing:
		*m = ModeEnforcing
	default:
		return fmt.Errorf("invalid authentication mode %q: valid values are %q, %q, %q", s, ModeDisabled, ModePermissive, ModeEnforcing)
	}
	return nil
}
