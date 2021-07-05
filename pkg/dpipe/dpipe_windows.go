package dpipe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
)

const peSize = uint32(unsafe.Sizeof(windows.ProcessEntry32{}))

type processInfo struct {
	pid  uint32
	ppid uint32
	exe  string
}

func waitCloseAndKill(ctx context.Context, cmd *dexec.Cmd, peer io.Closer, closing *int32, _ **time.Timer) {
	<-ctx.Done()
	atomic.StoreInt32(closing, 1)

	_ = peer.Close()

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
	} else {
		if err = terminateProcess(ctx, cmd.Path, uint32(cmd.Process.Pid), pes); err != nil {
			dlog.Error(ctx, err)
		}
	}
}

// terminateProcess will terminate the given process and all its children. The
// children are terminated first.
func terminateProcess(ctx context.Context, exe string, pid uint32, pes []*processInfo) error {
	if err := terminateChildrenOf(ctx, pid, pes); err != nil {
		return err
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
		alreadyDead := false
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			alive, aliveErr := processIsAlive(pid)
			if aliveErr != nil {
				dlog.Error(ctx, err)
			} else {
				alreadyDead = !alive
			}
		}
		if !alreadyDead {
			return fmt.Errorf("failed to terminate %q: %w", exe, err)
		}
	}
	dlog.Infof(ctx, "terminated process %q (pid %d)", exe, pid)
	return nil
}

func terminateChildrenOf(ctx context.Context, pid uint32, pes []*processInfo) error {
	for _, pe := range pes {
		if pe.ppid == pid {
			if err := terminateProcess(ctx, pe.exe, pe.pid, pes); err != nil {
				return err
			}
		}
	}
	return nil
}

// processIsAlive checks if the given pid exists in the current process snapshot
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
