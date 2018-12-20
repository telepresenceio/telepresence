package tpu

import (
	"os/exec"
	"strings"
)

func ShellLog(command string, logln func(string)) (string, error) {
	return CmdLog([]string{"sh", "-c", command}, logln)
}

func Cmd(command ...string) (string, error) {
	return CmdLog(command, func(string) {})
}

func CmdLogf(command []string, logf func(string, ...interface{})) (string, error) {
	return CmdLog(command, func(line string) { logf("%s", line) })
}

func CmdLog(command []string, logln func(string)) (string, error) {
	logln(strings.Join(command, " "))
	cmd := exec.Command(command[0], command[1:]...)
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
