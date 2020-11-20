package client

import (
	"fmt"
	"os/exec"
)

// RunError checks if the given err is a *exit.ExitError, and if so, extracts
// Stderr and the ExitCode from it.
func RunError(err error) error {
	if ee, ok := err.(*exec.ExitError); ok {
		if len(ee.Stderr) > 0 {
			err = fmt.Errorf("%s, exit code %d", string(ee.Stderr), ee.ExitCode())
		} else {
			err = fmt.Errorf("exit code %d", ee.ExitCode())
		}
	}
	return err
}
