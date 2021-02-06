package logging

import (
	"os"
	"path/filepath"
)

func dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return filepath.Join(home, "Library", "Logs", "telepresence")
}
