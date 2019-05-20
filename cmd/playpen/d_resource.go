package main

import (
	"fmt"
	"time"

	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/pkg/errors"
)

// LogfE logs a message and then returns the same message as an error
func LogfE(p *supervisor.Process, format string, a ...interface{}) error {
	msg := fmt.Sprintf(format, a...)
	p.Log(msg)
	return errors.New(msg)
}

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
	sub           *Subprocess
	check         func(*supervisor.Process) error
	lastCheckedAt time.Time
	enabled       bool
	ok            bool
}

// NewCommandResource returns a new Command Resource with the specified name and
// command arguments
func NewCommandResource(name string, args []string) *CommandResource {
	return &CommandResource{
		sub: &Subprocess{
			Name: name,
			Args: args,
		},
	}
}

// SetCheckFunction lets you override the check function called by Monitor().
// The default verifies that the subprocess is still running.
func (cr *CommandResource) SetCheckFunction(check func(*supervisor.Process) error) {
	cr.check = check
}

// Name implements Resource
func (cr *CommandResource) Name() string {
	return cr.sub.Name
}

// Enable starts the subprocess if it is not already running. Once enabled,
// Monitor will try to keep the subprocess running.
func (cr *CommandResource) Enable(p *supervisor.Process) error {
	if cr.enabled {
		return fmt.Errorf("trying to enable already-enabled %s", cr.Name())
	}
	cr.enabled = true
	cr.ok = false
	p.Logf("Resource %s now enabled", cr.Name())
	return nil
}

// Monitor checks the current status of the resource by calling the specified
// check function or checking whether the subprocess is still running. If things
// are not okay, it'll start or kill-and-restart the subprocess. If things break
// very badly, it will return an error, in which case it probably makes sense to
// quit.
func (cr *CommandResource) Monitor(p *supervisor.Process) error {
	oldOkay := cr.ok
	defer func() {
		if cr.enabled && (cr.ok != oldOkay) {
			Notify(p, fmt.Sprintf("%s: %t -> %t", cr.Name(), oldOkay, cr.ok))
		}
	}()

	cr.ok = true
	if !cr.enabled {
		return nil
	}

	cr.lastCheckedAt = time.Now()
	if !cr.sub.Running() {
		cr.ok = false
		p.Logf("Resource %s is not running. Launching...", cr.Name())
		err := cr.sub.Start(p)
		if err != nil {
			p.Logf("Failed to launch %s. Giving up.", cr.Name())
			return err
		}
		// Launched. Try user checks next time around.
		return nil
	}

	if cr.check == nil {
		return nil
	}
	err := cr.check(p)
	if err == nil {
		return nil
	}

	cr.ok = false
	p.Logf("Resource %s failed user check: %v", cr.Name(), err)
	err = cr.sub.Kill(p)
	if err == nil {
		return nil
	}
	p.Logf("Failed to kill %s on user check fail: %v", cr.Name(), err)
	p.Log("Giving up.")
	return err
}

// Disable kills the subprocess if it is running and turns off monitoring
func (cr *CommandResource) Disable(p *supervisor.Process) error {
	cr.enabled = false
	cr.ok = true
	if !cr.sub.Running() {
		return nil
	}
	return cr.sub.Kill(p)
}

// IsEnabled returns whether this resource is enabled
func (cr *CommandResource) IsEnabled() bool {
	return cr.enabled
}

// IsOkay returns whether the resource is okay as far as monitoring is aware
func (cr *CommandResource) IsOkay() bool {
	return cr.ok
}
