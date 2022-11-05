//go:build !windows
// +build !windows

package logging

import (
	"time"

	"golang.org/x/term"
)

// restoreCTimeAfterRename is a noop on unixes since the renamed file retains the creation time of the source.
func restoreCTimeAfterRename(_ string, _ time.Time) error {
	return nil
}

// IsTerminal returns whether the given file descriptor is a terminal.
var IsTerminal = term.IsTerminal //nolint:gochecknoglobals // os specific func replacement
