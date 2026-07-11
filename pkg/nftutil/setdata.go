//go:build linux

package nftutil

import "github.com/google/nftables"

// SetData pairs an nftables set/map with its elements so callers can add
// every set/map an nftables.Conn needs, together with the rules that
// reference them, in a fixed order, when building an idempotent full-replace
// Apply batch (see pkg/agentnft and pkg/routenft, whose Apply functions add
// every SetData before any Rule that looks it up).
type SetData struct {
	Set      *nftables.Set
	Elements []nftables.SetElement
}
