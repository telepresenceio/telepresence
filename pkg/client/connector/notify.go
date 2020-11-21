package connector

import (
	"context"
	"fmt"
	"runtime"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
)

var (
	notifyEnabled = false
)

// Notify displays a desktop banner notification to the user
func Notify(c context.Context, message string) {
	dlog.Info(c, "----------------------------------------------------------------------")
	dlog.Infof(c, "NOTIFY: %s", message)
	dlog.Info(c, "----------------------------------------------------------------------")

	if !notifyEnabled {
		return
	}

	var exe string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf("display notification \"Telepresence Daemon\" with title \"%s\"", message)
		exe = "osascript"
		args = []string{"-e", script}
	case "linux":
		exe = "notify-send"
		args = []string{"Telepresence Daemon", message}
	default:
		return
	}

	cmd := dexec.CommandContext(c, exe, args...)
	if err := cmd.Run(); err != nil {
		dlog.Errorf(c, "ERROR while notifying: %v", err)
	}
}
