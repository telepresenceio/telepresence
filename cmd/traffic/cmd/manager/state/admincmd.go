package state

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/tmconfig"
)

func (s *State) runAdminCommand(c tmconfig.AdminCommand) error {
	switch c.Name {
	case tmconfig.RemoveIntercept:
		if len(c.Args) != 1 {
			return fmt.Errorf("%s requires exactly one argument", c.Name)
		}
		interceptID := c.Args[0]
		clog.Debugf(s.backgroundCtx, "Removing intercept with ID %q", interceptID)
		s.RemoveIntercept(interceptID)
		return nil
	default:
		return fmt.Errorf("unknown admin command %q", c.Name)
	}
}

// RunAdminCommands executes all admin commands that have a timestamp greater than the given lastRun.
func (s *State) RunAdminCommands(l tmconfig.AdminCommandList) error {
	now := time.Now().UnixNano()
	lri := atomic.LoadInt64(&s.lastAdminRun)
	if !atomic.CompareAndSwapInt64(&s.lastAdminRun, lri, now) {
		return nil
	}
	mostResent := int64(0)
	var errs error
	for _, cmd := range l {
		if cmd.Timestamp > mostResent {
			mostResent = cmd.Timestamp
		}
		if cmd.Timestamp > lri {
			errs = errors.Join(errs, s.runAdminCommand(cmd))
		}
	}
	if mostResent > 0 {
		// Delete all commands that are older than, or have the same age as, the most recent command found here.
		// This operation reloads the configmap and may retain more recent entries.
		errs = errors.Join(errs, tmconfig.ClearCommands(s.backgroundCtx, managerutil.GetEnv(s.backgroundCtx).ManagerNamespace, mostResent))
	}
	return errs
}
