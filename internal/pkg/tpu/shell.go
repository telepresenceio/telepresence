package tpu

import (
	"os/exec"
	"strings"
)

func Shell(command string) (result string, err error) {
	return ShellLog(command, func(string) {})
}

func ShellLog(command string, logln func(string)) (string, error) {
	logln(command)
	cmd := exec.Command("sh", "-c", command)
	out, err := cmd.CombinedOutput()
	str := string(out)
	lines := strings.Split(str, "\n")
	for idx, line := range lines {
		if strings.TrimSpace(line) != "" {
			logln(line)
		} else if idx != len(lines)-1 {
			logln(line)
		}
	}
	if err != nil {
		logln(err.Error())
	}
	return str, err
}

func ShellLogf(command string, logf func(string, ...interface{})) (string, error) {
	return ShellLog(command, func(line string) { logf("%s", line) })
}
