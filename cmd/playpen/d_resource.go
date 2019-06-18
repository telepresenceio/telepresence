package main

import (
	"fmt"
	"sync"
	"syscall"

	"github.com/datawire/teleproxy/pkg/supervisor"
)

// Resource represents one thing managed by playpen daemon. Examples include
// network intercepts (via teleproxy intercept) and cluster connectivity.
type Resource interface {
	Name() string
	Enable(*supervisor.Process) error
	Monitor(*supervisor.Process) error
	Disable(*supervisor.Process) error
	IsEnabled() bool
	IsOkay() bool
}

// CommandResource represents a resource that is associated with a running
// subprocess
type CommandResource struct {
	name    string
	args    []string
	rai     *RunAsInfo
	check   func(*supervisor.Process) error
	lock    sync.Mutex
	enabled bool            // (Enable/Disable) disabled by default
	broken  bool            // (Monitor) okay by default (disabled -> okay)
	cmd     *supervisor.Cmd // (run loop) tracks the cmd for killing it
	running bool            // (run loop) not running by default, of course
}

// NewCommandResource returns a new Command Resource with the specified name and
// command arguments
func NewCommandResource(name string, args []string, rai *RunAsInfo) *CommandResource {
	return &CommandResource{
		name: name,
		args: args,
		rai:  rai,
	}
}

// Name implements Resource
func (cr *CommandResource) Name() string {
	cr.lock.Lock()
	defer cr.lock.Unlock()
	return cr.name
}

// Enable marks the command resource as enabled. It launches a supervised
// goroutine (the runLoop) to keep a subprocess running for this command.
func (cr *CommandResource) Enable(p *supervisor.Process) error {
	cr.lock.Lock()
	defer cr.lock.Unlock()
	if cr.enabled {
		return fmt.Errorf("trying to enable already-enabled CR %s", cr.name)
	}
	cr.enabled = true
	p.Supervisor().Supervise(&supervisor.Worker{
		Name:  "run/" + cr.name,
		Work:  cr.runLoop,
		Retry: true,
	})
	return nil
}

// Disable marks the command resource as disabled. It then kills any running
// subprocess, which will allow the runLoop goroutine to finish.
func (cr *CommandResource) Disable(p *supervisor.Process) error {
	cr.lock.Lock()
	defer cr.lock.Unlock()
	if !cr.enabled {
		return fmt.Errorf("trying to disable already-disabled CR %s", cr.name)
	}
	cr.enabled = false
	if cr.running {
		return cr.cmd.Process.Signal(syscall.SIGTERM)
	}
	return nil
}

// Monitor updates the recorded status of the command resource.
func (cr *CommandResource) Monitor(p *supervisor.Process) error {
	cr.lock.Lock()
	defer cr.lock.Unlock()

	// Determine and record whether the resource is broken
	cr.broken = func() bool {
		if !cr.enabled {
			// Disabled resources are always okay
			return false
		}
		if !cr.running {
			// Not running is broken. The runLoop will restart the process at
			// some point soon.
			return true
		}
		if cr.check == nil {
			// No additional check, so just running is okay
			return false
		}
		err := cr.check(p)
		if err == nil {
			// Check passed
			return false
		}
		p.Logf("Resource %s failed check: %v", cr.name, err)
		return true
	}()

	// Kill the process if it's in a bad state
	if cr.broken && cr.running {
		err := cr.cmd.Process.Signal(syscall.SIGTERM)
		if err != nil {
			// Failure to kill is a fatal error
			// FIXME: This will be a problem if the resource is in the process
			// of dying when we user-check it, but is dead when we get around to
			// killing it.
			p.Logf("Failed to kill %s on check fail: %v", cr.name, err)
			p.Log("Giving up.")
			return err
		}
		// The runLoop will restart the process, at which point we hope things
		// will be better.
	}

	return nil
}

// IsEnabled returns whether this resource is enabled
func (cr *CommandResource) IsEnabled() bool {
	cr.lock.Lock()
	defer cr.lock.Unlock()
	return cr.enabled
}

// IsOkay returns whether the resource is okay as far as monitoring is aware
func (cr *CommandResource) IsOkay() bool {
	cr.lock.Lock()
	defer cr.lock.Unlock()
	return !cr.broken
}

// SetCheck sets the check function that will be called by Monitor()
func (cr *CommandResource) SetCheck(check func(*supervisor.Process) error) {
	cr.lock.Lock()
	defer cr.lock.Unlock()
	cr.check = check
}

// runLoop runs the command and manages cr.cmd, cr.running. Supervisor does the
// actual looping thanks to the "Retry" parameter.
func (cr *CommandResource) runLoop(p *supervisor.Process) error {
	if !cr.IsEnabled() {
		return nil // Indicate success so Supervisor does not retry
	}

	cr.cmd = cr.rai.Command(p, cr.args...)
	err := cr.cmd.Start()
	if err != nil {
		return err
	}
	p.Ready()

	cr.running = true
	err = p.DoClean(cr.cmd.Wait, func() error { return cr.Disable(p) })
	cr.running = false

	// Return an error so Supervisor retries
	return fmt.Errorf("%s subprocessed ended: %v", cr.Name(), err)
}
