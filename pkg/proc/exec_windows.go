package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec" //nolint:depguard // We want no logging and no soft-context signal handling
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

var SignalsToForward = []os.Signal{os.Interrupt} //nolint:gochecknoglobals // OS-specific constant list

// SIGTERM uses os.Interrupt on Windows as a best effort.
var SIGTERM = os.Interrupt //nolint:gochecknoglobals // OS-specific constant

func CommandContext(ctx context.Context, name string, args ...string) *dexec.Cmd {
	cmd := dexec.CommandContext(ctx, name, args...)
	createNewProcessGroup(cmd.Cmd)
	return cmd
}

func createNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func cacheAdmin(_ context.Context, _ string) error {
	// No-op on windows, there's no sudo caching. Runas will just pop a window open.
	return nil
}

func startInBackground(_ bool, args ...string) error {
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
		argsStr := shellquote.ShellArgsString(args)
		argPtr, _ = windows.UTF16PtrFromString(argsStr)
	}
	return windows.ShellExecute(0, verbPtr, exePtr, argPtr, cwdPtr, windows.SW_HIDE)
}

func isAdmin() bool {
	// Directly copied from the official Windows documentation. The Go API for this is a
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

func terminate(p *os.Process) error {
	return p.Kill()
}

const peSize = uint32(unsafe.Sizeof(windows.ProcessEntry32{}))

type processInfo struct {
	pid  uint32
	ppid uint32
	exe  string
}

func killProcessGroup(ctx context.Context, cmd *exec.Cmd, sig os.Signal) {
	pes := make([]*processInfo, 0, 100)
	err := eachProcess(func(pe *windows.ProcessEntry32) bool {
		pes = append(pes, &processInfo{
			pid:  pe.ProcessID,
			ppid: pe.ParentProcessID,
			exe:  windows.UTF16ToString(pe.ExeFile[:]),
		})
		return true
	})
	if err != nil {
		dlog.Error(ctx, err)
	} else if err = terminateProcess(ctx, cmd.Path, uint32(cmd.Process.Pid), sig, pes); err != nil {
		dlog.Error(ctx, err)
	}
}

// terminateProcess will terminate the given process and all its children. The
// children are terminated first.
func terminateProcess(ctx context.Context, exe string, pid uint32, sig os.Signal, pes []*processInfo) error {
	if err := terminateChildrenOf(ctx, pid, sig, pes); err != nil {
		return err
	}

	if sig == os.Interrupt {
		if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, pid); err != nil {
			// An ACCESS_DENIED error may indicate that the process is dead already but
			// died just after the handle to it was opened.
			if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				if alive, aliveErr := processIsAlive(pid); aliveErr != nil {
					dlog.Error(ctx, aliveErr)
				} else if !alive {
					return nil
				}
			}
			return fmt.Errorf("%q: %w", exe, &os.SyscallError{Syscall: "GenerateConsoleCtrlEvent", Err: err})
		}
		dlog.Debugf(ctx, "sent ctrl-c to process %q (pid %d)", exe, pid)
		return nil
	}

	// SYNCHRONIZE is required to wait for the process to terminate
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, true, pid)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			// ERROR_INVALID_PARAMETER means that the process no longer exists. It might
			// have died because we killed its children.
			return nil
		}
		return fmt.Errorf("failed to open handle of %q: %w", exe, err)
	}
	defer func() {
		_ = windows.CloseHandle(h)
	}()

	if err = windows.TerminateProcess(h, 0); err != nil {
		// An ACCESS_DENIED error may indicate that the process is dead already but
		// died just after the handle to it was opened.
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			if alive, aliveErr := processIsAlive(pid); aliveErr != nil {
				dlog.Error(ctx, aliveErr)
			} else if !alive {
				return nil
			}
		}
		return fmt.Errorf("%q: %w", exe, &os.SyscallError{Syscall: "TerminateProcess", Err: err})
	}
	dlog.Debugf(ctx, "terminated process %q (pid %d)", exe, pid)
	return nil
}

func terminateChildrenOf(ctx context.Context, pid uint32, sig os.Signal, pes []*processInfo) error {
	for _, pe := range pes {
		if pe.ppid == pid {
			if err := terminateProcess(ctx, pe.exe, pe.pid, sig, pes); err != nil {
				return err
			}
		}
	}
	return nil
}

// processIsAlive checks if the given pid exists in the current process snapshot.
func processIsAlive(pid uint32) (bool, error) {
	found := false
	err := eachProcess(func(pe *windows.ProcessEntry32) bool {
		if pe.ProcessID == pid {
			found = true
			return false // break iteration
		}
		return true
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

// eachProcess calls the given function with each ProcessEntry32 found
// in the current process snapshot. The iteration ends if the given function
// returns false.
func eachProcess(f func(pe *windows.ProcessEntry32) bool) error {
	h, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return fmt.Errorf("unable to get process snapshot: %w", err)
	}
	defer func() {
		_ = windows.CloseHandle(h)
	}()
	pe := new(windows.ProcessEntry32)
	pe.Size = peSize
	err = windows.Process32First(h, pe)
	for err == nil {
		if !f(pe) {
			break
		}
		err = windows.Process32Next(h, pe)
	}
	return nil
}
