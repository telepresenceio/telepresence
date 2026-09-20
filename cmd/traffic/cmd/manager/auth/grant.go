package auth

import (
	"fmt"
	"strings"
)

// Grant identifies which RBAC grant authorizes a client operation, such as
// connecting or attaching to a workload.
type Grant string

const (
	// GrantPortForward reviews only pods/portforward, the mechanism a client
	// physically exercises to reach the manager or an agent pod.
	GrantPortForward Grant = "portforward"

	// GrantTelepresence reviews only the telepresence.io group's own
	// attributes (connections, attachments), independent of pods/portforward.
	GrantTelepresence Grant = "telepresence"

	// GrantAny accepts either review: it tries the telepresence.io attributes
	// first and falls back to pods/portforward.
	GrantAny Grant = "any"
)

func (g Grant) String() string {
	return string(g)
}

// UnmarshalText parses a Grant, case-insensitively. An empty value defaults
// to GrantAny.
func (g *Grant) UnmarshalText(text []byte) error {
	s := string(text)
	if s == "" {
		*g = GrantAny
		return nil
	}
	grant := Grant(strings.ToLower(s))
	switch grant {
	case GrantPortForward, GrantTelepresence, GrantAny:
		*g = grant
	default:
		return fmt.Errorf("invalid required grant %q: valid values are %q, %q, %q", s, GrantPortForward, GrantTelepresence, GrantAny)
	}
	return nil
}
