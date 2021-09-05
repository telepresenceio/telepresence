//go:build !windows
// +build !windows

package logging

import (
	"io/fs"
	"os"

	"golang.org/x/term"
)

// createFile creates a new file or truncates an existing file.
func createFile(fullPath string, perm fs.FileMode) (*os.File, error) {
	return os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
}

// openForAppend opens a file for append or creates it if it doesn't exist.
func openForAppend(logfilePath string, perm fs.FileMode) (*os.File, error) {
	return os.OpenFile(logfilePath, os.O_WRONLY|os.O_APPEND, perm)
}

// IsTerminal returns whether the given file descriptor is a terminal
var IsTerminal = term.IsTerminal
