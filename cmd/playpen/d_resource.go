package main

import (
	"fmt"
	"syscall"
	"time"

	"github.com/datawire/teleproxy/pkg/supervisor"
)

// Resource represents one thing managed by playpen daemon. Examples include
// network intercepts (via teleproxy intercept) and cluster connectivity.
type Resource interface {
	Name() string
	IsOkay() bool
	Close() error
}

// crCmd is a handle to a checked retrying command
type crCmd struct {
	name     string
	args     []string
	rai      *RunAsInfo
	check    func() error
	tasks    chan func() error
	callerP  *supervisor.Process // processor's Process
	cmd      *supervisor.Cmd     // (run loop) tracks the cmd for killing it
	quitting bool                // (Close) to get everything to quit
	okay     bool                // (monitor) cmd is running and check passes
}

// CheckedRetryingCommand launches a command, restarting it repeatedly if it
// quits, and killing and restarting it if it fails the given check.
func CheckedRetryingCommand(
	p *supervisor.Process, name string, args []string, rai *RunAsInfo, check func() error,
) (Resource, error) {
	if check == nil {
		check = func() error { return nil }
	}
	crc := &crCmd{
		name:    name,
		args:    args,
		rai:     rai,
		check:   check,
		tasks:   make(chan func() error, 1),
		callerP: p,
	}
	p.Supervisor().Supervise(&supervisor.Worker{
		Name: "crc/" + crc.name,
		Work: crc.processor,
	})
	if err := crc.launch(); err != nil {
		return nil, err
	}
	return crc, nil
}

// Name implements Resource
func (crc *crCmd) Name() string {
	res := make(chan string)
	crc.tasks <- func() error {
		res <- crc.name
		return nil
	}
	return <-res
}

// IsOkay returns whether the resource is okay as far as monitoring is aware
func (crc *crCmd) IsOkay() bool {
	res := make(chan bool)
	crc.tasks <- func() error {
		res <- crc.okay
		return nil
	}
	return <-res
}

// Close shuts down this resource
func (crc *crCmd) Close() error {
	done := make(chan struct{})
	crc.tasks <- func() error {
		defer close(done)
		return crc.quit()
	}
	<-done

	// FIXME: Wait until things have closed?
	return nil
}

func (crc *crCmd) processor(p *supervisor.Process) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	p.Ready()
	for {
		var task func() error
		select {
		case fn := <-crc.tasks: // There is work to do
			task = fn
		case <-ticker.C: // Ticker says it's time to monitor
			task = func() error { return crc.monitor(p) }
		case <-p.Shutdown(): // Supervisor told us to quit
			task = crc.quit
		}
		if err := task(); err != nil {
			return err
		}
		if crc.quitting {
			return nil
		}
	}
}

func (crc *crCmd) launch() error {
	if crc.cmd != nil {
		panic(fmt.Errorf("launching %s: already launched", crc.name))
	}
	launchErr := make(chan error)
	crc.callerP.Supervisor().Supervise(&supervisor.Worker{
		Name: "proc/" + crc.name,
		Work: func(p *supervisor.Process) error {
			// Launch the subprocess
			crc.cmd = crc.rai.Command(p, crc.args...)
			if err := crc.cmd.Start(); err != nil {
				launchErr <- err
				return nil
			}
			launchErr <- nil
			p.Ready()

			// Wait for the subprocess to end, log
			p.Logf("subprocess ended: %v", p.DoClean(crc.cmd.Wait, crc.kill))
			crc.cmd = nil

			return nil
		},
	})
	select {
	case err := <-launchErr:
		return err
	case <-crc.callerP.Shutdown():
		return nil
	}
}

func (crc *crCmd) kill() error {
	if crc.cmd != nil {
		return crc.cmd.Process.Signal(syscall.SIGTERM)
	}
	return nil // Or fmt.Errorf("trying to kill non-running subprocess for %s", crc.name)
}

func (crc *crCmd) quit() error {
	crc.quitting = true
	return crc.kill()
}

// monitor determines and records whether the resource is okay
func (crc *crCmd) monitor(p *supervisor.Process) error {
	defer func(old bool) { MaybeNotify(p, crc.name, old, crc.okay) }(crc.okay)
	if crc.cmd == nil {
		crc.okay = false // Not running is not okay
		crc.tasks <- crc.launch
		return nil
	}
	if err := crc.check(); err != nil {
		crc.okay = false // Check failed is not okay
		p.Logf("check failed: %v", err)
		// Kill the process because it's in a bad state
		if err := crc.kill(); err != nil {
			// Failure to kill is a fatal error
			// FIXME: This will be a problem if the resource is in the process
			// of dying when we user-check it, but is dead when we get around to
			// killing it.
			p.Logf("failed to kill: %v", err)
			return err
		}
	}
	crc.okay = true
	return nil
}
