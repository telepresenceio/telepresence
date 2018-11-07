package tpu

import (
	"log"
	"os/exec"
)

func Shell(command string) (result string, err error) {
	return shell(command, false)
}

func ShellQ(command string) (result string, err error) {
	return shell(command, true)
}

func shell(command string, quiet bool) (string, error) {
	if !quiet {
		log.Println(command)
	}
	cmd := exec.Command("sh", "-c", command)
	out, err := cmd.CombinedOutput()
	str := string(out)
	if !quiet {
		log.Print(str)
		if err != nil {
			log.Println(err)
		}
	}
	return str, err
}
