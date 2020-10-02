package connector

import (
	"fmt"
	"runtime"

	"github.com/datawire/ambassador/pkg/supervisor"
)

var (
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

	var exe string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf("display notification \"Edge Control Daemon\" with title \"%s\"", message)
		exe = "osascript"
		args = []string{"-e", script}
	case "linux":
		exe = "notify-send"
		args = []string{"Edge Control Daemon", message}
	default:
		return
	}

	cmd := p.Command(exe, args...)
	if err := cmd.Run(); err != nil {
		p.Logf("ERROR while notifying: %v", err)
	}
}
