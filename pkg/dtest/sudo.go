package dtest

import (
	"fmt"
	"os"
	"os/exec"
)

// Sudo is intended for use in a TestMain. It will relaunch the test
// executable via sudo if it isn't already running with an effective
// userid of root.
func Sudo() {
	/* #nosec */
	if os.Geteuid() != 0 {
		cmd := exec.Command("sudo", append([]string{"-E"}, os.Args...)...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			fmt.Print(err)
		}
		os.Exit(cmd.ProcessState.ExitCode())
	}
}
