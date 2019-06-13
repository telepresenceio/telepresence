package dtest

import (
	"fmt"
	"os"
	"os/exec"
)

func Sudo() {
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
