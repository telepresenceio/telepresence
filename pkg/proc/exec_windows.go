package proc

import (
	"context"
	"os"

	"golang.org/x/sys/windows"

	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
)

var signalsToForward = []os.Signal{os.Interrupt}

func startInBackground(args ...string) error {
	return shellExec("open", args[0], args[1:]...)
}

func startInBackgroundAsRoot(_ context.Context, args ...string) error {
	verb := "runas"
	if isAdmin() {
		verb = "open"
	}
	return shellExec(verb, args[0], args[1:]...)
}

func shellExec(verb, exe string, args ...string) error {
	cwd, _ := os.Getwd()
	// UTF16PtrFromString can only fail if the argument contains a NUL byte. That will never happen here.
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

func isAdmin() bool {
	// Directly copied from the official windows documentation. The Go API for this is a
	// direct wrap around the official C++ API.
	// See https://docs.microsoft.com/en-us/windows/desktop/api/securitybaseapi/nf-securitybaseapi-checktokenmembership
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid)
	if err != nil {
		return false
	}
	adm, err := windows.GetCurrentProcessToken().IsMember(sid)
	return err == nil && adm
}
