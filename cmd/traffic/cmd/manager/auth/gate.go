package auth

import (
	"fmt"
	"strings"
)

// Gate controls which grant the manager accepts as authorization to connect
// and to attach to a workload.
type Gate string

const (
	// GatePortForward reviews only pods/portforward, the mechanism a client
	// physically exercises to reach the manager or an agent pod.
	GatePortForward Gate = "portforward"

	// GateTelepresence reviews only the telepresence.io group's own
	// attributes (connections, attachments), independent of pods/portforward.
	GateTelepresence Gate = "telepresence"

	// GateAny accepts either review: it tries the telepresence.io attributes
	// first and falls back to pods/portforward.
	GateAny Gate = "any"
)

func (g Gate) String() string {
	return string(g)
}

// UnmarshalText parses a Gate, case-insensitively. An empty value defaults to
// GateAny.
func (g *Gate) UnmarshalText(text []byte) error {
	s := string(text)
	if s == "" {
		*g = GateAny
		return nil
	}
	switch Gate(strings.ToLower(s)) {
	case GatePortForward:
		*g = GatePortForward
	case GateTelepresence:
		*g = GateTelepresence
	case GateAny:
		*g = GateAny
	default:
		return fmt.Errorf("invalid authorization gate %q: valid values are %q, %q, %q", s, GatePortForward, GateTelepresence, GateAny)
	}
	return nil
}
