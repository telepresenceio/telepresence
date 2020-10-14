package edgectl

import (
	"fmt"
	"syscall"
	"time"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/supervisor"
)

// crCmd is a handle to a checked retrying command
type crCmd struct {
	exe        string
	args       []string
	check      func(p *supervisor.Process) error
	startGrace time.Duration
	cmd        *supervisor.Cmd // (run loop) tracks the cmd for killing it
	quitting   bool            // (run loop) enables Close()
	startedAt  time.Time
	ResourceBase
}

// CheckedRetryingCommand launches a command, restarting it repeatedly if it
// quits, and killing and restarting it if it fails the given check.
func CheckedRetryingCommand(
	p *supervisor.Process, name string, exe string, args []string,
	check func(*supervisor.Process) error, startGrace time.Duration,
) (Resource, error) {
	if check == nil {
		check = func(*supervisor.Process) error { return nil }
	}
	crc := &crCmd{
		exe:        exe,
		args:       args,
		check:      check,
		startGrace: startGrace,
	}
	crc.Setup(p.Supervisor(), name, crc.doCheck, crc.doQuit)

	if err := crc.launch(p); err != nil {
		return nil, errors.Wrapf(err, "initial launch of %s", name)
	}
	return crc, nil
}

func (crc *crCmd) subprocessEnded(p *supervisor.Process) error {
	p.Log("end: subprocess ended")
	crc.cmd = nil
	if crc.quitting {
		p.Log("end: marking as done")
		crc.SetDone()
	}
	return nil
}

func (crc *crCmd) launch(p *supervisor.Process) error {
	if crc.cmd != nil {
		panic(fmt.Errorf("launching %s: already launched", crc.Name()))
	}

	// Launch the subprocess (set up logging using a worker)
	p.Logf("Launching %s...", crc.Name())
	launchErr := make(chan error)
	p.Supervisor().Supervise(&supervisor.Worker{
		Name: crc.Name() + "/out",
		Work: func(p *supervisor.Process) error {
			crc.cmd = p.Command(crc.exe, crc.args...)
			launchErr <- crc.cmd.Start()
			// Wait for the subprocess to end. Another worker will
			// call kill() on shutdown (via quit()) so we don't need
			// to worry about supervisor shutdown ourselves.
			if err := crc.cmd.Wait(); err != nil {
				p.Log(err)
			}
			crc.AddTask(crc.subprocessEnded)
			return nil
		},
	})

	// Wait for it to start
	select {
	case err := <-launchErr:
		if err != nil {
			return err
		}
	case <-p.Shutdown():
		return nil
	}
	crc.startedAt = time.Now()
	p.Logf("Launched %s", crc.Name())

	return nil
}

func (crc *crCmd) kill(p *supervisor.Process) error {
	if crc.cmd != nil {
		p.Log("kill: sending signal")
		if err := crc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			p.Logf("kill: failed (ignoring): %v", err)
		}
	} else {
		p.Log("kill: no subprocess to kill")
	}
	return nil
}

func (crc *crCmd) doQuit(p *supervisor.Process) error {
	crc.quitting = true
	return crc.kill(p)
}

// doCheck determines whether the subprocess is running and healthy
func (crc *crCmd) doCheck(p *supervisor.Process) error {
	if crc.cmd == nil {
		if crc.quitting {
			p.Log("check: no subprocess + quitting -> done")
			crc.SetDone()
			return nil
		}
		p.Log("check: no subprocess -> launch")
		crc.AddTask(crc.launch)
		return errors.New("not running")
	}
	if err := crc.check(p); err != nil {
		p.Logf("check: failed: %v", err)
		runTime := time.Since(crc.startedAt)
		if runTime > crc.startGrace {
			// Kill the process because it's in a bad state
			p.Log("check: killing...")
			_ = crc.kill(p)
		} else {
			p.Logf("check: not killing yet (%v < %v)", runTime, crc.startGrace)
		}
		return err // from crc.check() above
	}
	p.Log("check: passed")
	return nil
}
