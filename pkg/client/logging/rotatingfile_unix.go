//go:build !windows
// +build !windows

package logging

import (
	"os"

	"golang.org/x/term"
)

// createFile creates a new file or truncates an existing file.
func createFile(fullPath string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
}

// openForAppend opens a file for append or creates it if it doesn't exist.
func openForAppend(logfilePath string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(logfilePath, os.O_WRONLY|os.O_APPEND, perm)
}

// IsTerminal returns whether the given file descriptor is a terminal
var IsTerminal = term.IsTerminal

func (rf *RotatingFile) afterOpen() {
	if rf.captureStd {
		err := dupToStd(rf.file)
		if err != nil {
			// Dup2 failed
			os.Stdout = rf.file
			os.Stderr = rf.file
		} else {
			if os.Stdout.Fd() != 1 {
				os.Stdout = rf.file
			}
			if os.Stderr.Fd() != 2 {
				os.Stderr = rf.file
			}
		}
	}
	go rf.removeOldFiles()
}
