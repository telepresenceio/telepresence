package cliutil

import (
	"context"
	"os"
	"os/signal"

	"golang.org/x/sys/windows"

	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func background(ctx context.Context, exe string, args []string) error {
	return shellExec(ctx, "open", exe, args)
}

func backgroundAsRoot(ctx context.Context, exe string, args []string) error {
	verb := "runas"
	if proc.IsAdmin() {
		verb = "open"
	}
	return shellExec(ctx, verb, exe, args)
}

func shellExec(_ context.Context, verb, exe string, args []string) error {
	cwd, _ := os.Getwd()
	verbPtr, _ := windows.UTF16PtrFromString(verb)
	exePtr, _ := windows.UTF16PtrFromString(exe)
	cwdPtr, _ := windows.UTF16PtrFromString(cwd)
	var argPtr *uint16
	if len(args) > 0 {
		argsStr := logging.ShellArgsString(args)
		argPtr, _ = windows.UTF16PtrFromString(argsStr)
	}
	return windows.ShellExecute(0, verbPtr, exePtr, argPtr, cwdPtr, windows.SW_HIDE)
}

func signalNotifications() chan os.Signal {
	// Ensure that interrupt is propagated to the child process
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	return sigCh
}
