package main

import (
	"fmt"
	"runtime"

	"github.com/datawire/ambassador/pkg/supervisor"
)

var (
	notifyRAI     *RunAsInfo
	notifyEnabled = false
)

// Notify displays a desktop banner notification to the user
func Notify(p *supervisor.Process, message string) {
	p.Logf("----------------------------------------------------------------------")
	p.Logf("NOTIFY: %s", message)
	p.Logf("----------------------------------------------------------------------")

	if !notifyEnabled {
		return
	}

	if notifyRAI == nil {
		var err error
		notifyRAI, err = GuessRunAsInfo(p)
		if err != nil {
			p.Log(err)
			notifyRAI = &RunAsInfo{}
		}
	}

	var args []string
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf("display notification \"Edge Control Daemon\" with title \"%s\"", message)
		args = []string{"osascript", "-e", script}
	case "linux":
		args = []string{"notify-send", "Edge Control Daemon", message}
	default:
		return
	}

	cmd := notifyRAI.Command(p, args...)
	if err := cmd.Run(); err != nil {
		p.Logf("ERROR while notifying: %v", err)
	}
}
