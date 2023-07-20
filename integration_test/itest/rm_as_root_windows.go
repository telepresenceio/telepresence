package itest

import (
	"context"
	"os"

	"golang.org/x/sys/windows"

	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

func rmAsRoot(ctx context.Context, file string) error {
	cwd, _ := os.Getwd()
	// UTF16PtrFromString can only fail if the argument contains a NUL byte. That will never happen here.
	verbPtr, _ := windows.UTF16PtrFromString("runas")
	exePtr, _ := windows.UTF16PtrFromString("del")
	cwdPtr, _ := windows.UTF16PtrFromString(cwd)
	argPtr, _ := windows.UTF16PtrFromString(shellquote.ShellArgsString(append([]string{"/f", "/q"}, file)))
	return windows.ShellExecute(0, verbPtr, exePtr, argPtr, cwdPtr, windows.SW_HIDE)
}
