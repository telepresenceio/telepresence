package client

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/pkg/errors"
)

// crCmd is a handle to a checked retrying command
type crCmd struct {
	exe        string
	args       []string
	check      func(c context.Context) error
	startGrace time.Duration
	cmd        *dexec.Cmd // (run loop) tracks the cmd for killing it
	quitting   bool       // (run loop) enables Close()
	startedAt  time.Time
	ResourceBase
}

// CheckedRetryingCommand launches a command, restarting it repeatedly if it
// quits, and killing and restarting it if it fails the given check.
func CheckedRetryingCommand(
	c context.Context, name string, exe string, args []string,
	check func(context.Context) error, startGrace time.Duration,
) (Resource, error) {
	if check == nil {
		check = func(context.Context) error { return nil }
	}
	crc := &crCmd{
		exe:        exe,
		args:       args,
		check:      check,
		startGrace: startGrace,
	}
	crc.Setup(c, name, crc.doCheck, crc.doQuit)

	if err := crc.launch(c); err != nil {
		return nil, errors.Wrapf(err, "initial launch of %s", name)
	}
	return crc, nil
}

func (crc *crCmd) subprocessEnded(c context.Context) error {
	dlog.Debug(c, "end: subprocess ended")
	crc.cmd = nil
	if crc.quitting {
		dlog.Debug(c, "end: marking as done")
		crc.SetDone()
	}
	return nil
}

func (crc *crCmd) launch(c context.Context) error {
	if crc.cmd != nil {
		panic(fmt.Errorf("launching %s: already launched", crc.Name()))
	}

	dlog.Infof(c, "Launching %s...", crc.Name())
	launchErr := make(chan error)
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go(crc.Name(), func(c context.Context) error {
		crc.cmd = dexec.CommandContext(c, crc.exe, crc.args...)
		err := crc.cmd.Start()
		launchErr <- err
		// Wait for the subprocess to end. Another worker will
		// call kill() on shutdown (via quit()) so we don't need
		// to worry about shutdown ourselves.
		err = crc.cmd.Wait()
		crc.AddTask(crc.subprocessEnded)
		return err
	})
	if err := <-launchErr; err != nil {
		return err
	}
	crc.startedAt = time.Now()
	dlog.Infof(c, "Launched %s", crc.Name())
	return nil
}

func (crc *crCmd) kill(c context.Context) error {
	if crc.cmd != nil {
		dlog.Debug(c, "kill: sending signal")
		if err := crc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			dlog.Debugf(c, "kill: failed (ignoring): %v", err)
		}
	} else {
		dlog.Debug(c, "kill: no subprocess to kill")
	}
	return nil
}

func (crc *crCmd) doQuit(c context.Context) error {
	crc.quitting = true
	return crc.kill(c)
}

// doCheck determines whether the subprocess is running and healthy
func (crc *crCmd) doCheck(c context.Context) error {
	if crc.cmd == nil {
		if crc.quitting {
			dlog.Debug(c, "check: no subprocess + quitting -> done")
			crc.SetDone()
			return nil
		}
		dlog.Debug(c, "check: no subprocess -> launch")
		crc.AddTask(crc.launch)
		return errors.New("not running")
	}
	if err := crc.check(c); err != nil {
		dlog.Debugf(c, "check: failed: %v", err)
		runTime := time.Since(crc.startedAt)
		if runTime > crc.startGrace {
			// Kill the process because it's in a bad state
			dlog.Debug(c, "check: killing...")
			_ = crc.kill(c)
		} else {
			dlog.Debugf(c, "check: not killing yet (%v < %v)", runTime, crc.startGrace)
		}
		return err // from crc.check() above
	}
	return nil
}
