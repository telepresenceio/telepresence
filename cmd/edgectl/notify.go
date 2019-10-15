package main

import (
	"fmt"
	"runtime"

	"github.com/datawire/ambassador/pkg/supervisor"
)

var notifyRAI *RunAsInfo

// Notify displays a desktop banner notification to the user
func Notify(p *supervisor.Process, message string) {
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

	p.Logf("NOTIFY: %s", message)
	cmd := notifyRAI.Command(p, args...)
	if err := cmd.Run(); err != nil {
		p.Logf("ERROR while notifying: %v", err)
	}
}

// MaybeNotify displays a notification only if a value changes
func MaybeNotify(p *supervisor.Process, name string, old, new bool) {
	if old != new {
		Notify(p, fmt.Sprintf("%s: %t -> %t", name, old, new))
	}
}
