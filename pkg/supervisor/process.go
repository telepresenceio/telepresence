package supervisor

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync/atomic"

	"github.com/pkg/errors"
)

// A Process represents a goroutine being run from a Worker.
type Process struct {
	supervisor *Supervisor
	worker     *Worker
	// Used to signal graceful shutdown.
	shutdown       chan struct{}
	ready          bool
	shutdownClosed bool
}

// Supervisor returns the Supervisor that is managing this Process.
func (p *Process) Supervisor() *Supervisor {
	return p.supervisor
}

// Worker returns the Worker that this Process is running.
func (p *Process) Worker() *Worker {
	return p.worker
}

// Context returns the Process' context.
func (p *Process) Context() context.Context {
	return p.supervisor.context
}

// Ready is called by the Process' Worker to notify the supervisor
// that it is now ready.
func (p *Process) Ready() {
	p.Supervisor().change(func() {
		p.ready = true
	})
}

// Shutdown is used for graceful shutdown...
func (p *Process) Shutdown() <-chan struct{} {
	return p.shutdown
}

// Log is used for logging...
func (p *Process) Log(obj interface{}) {
	p.supervisor.Logger.Printf("%s: %v", p.Worker().Name, obj)
}

// Logf is used for logging...
func (p *Process) Logf(format string, args ...interface{}) {
	p.supervisor.Logger.Printf("%s: %v", p.Worker().Name, fmt.Sprintf(format, args...))
}

func (p *Process) allocateID() int64 {
	return atomic.AddInt64(&p.Worker().children, 1)
}

// Go is shorthand for launching a child worker... it is named
// "<parent>[<child-count>]".
func (p *Process) Go(fn func(*Process) error) *Worker {
	w := &Worker{
		Name: fmt.Sprintf("%s[%d]", p.Worker().Name, p.allocateID()),
		Work: fn,
	}
	p.Supervisor().Supervise(w)
	return w
}

// GoName is shorthand for launching a named worker... it is named
// "<parent>.<name>".
func (p *Process) GoName(name string, fn func(*Process) error) *Worker {
	w := &Worker{
		Name: fmt.Sprintf("%s.%s", p.Worker().Name, name),
		Work: fn,
	}
	p.Supervisor().Supervise(w)
	return w
}

// Do is shorthand for proper shutdown handling while doing a
// potentially blocking activity. This method will return nil if the
// activity completes normally and an error if the activity panics or
// returns an error.
//
// If you want to know whether the work was aborted or might still be
// running when Do returns, then use DoClean like so:
//
//   aborted := errors.New("aborted")
//
//   err := p.DoClean(..., func() { return aborted })
//
//   if err == aborted {
//     ...
//   }
func (p *Process) Do(fn func() error) (err error) {
	return p.DoClean(fn, func() error { return nil })
}

// DoClean is the same as Process.Do() but executes the supplied clean
// function on abort.
func (p *Process) DoClean(fn, clean func() error) (err error) {
	sup := p.Supervisor()
	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				stack := string(debug.Stack())
				err := errors.Errorf("FUNCTION PANICKED: %v\n%s", r, stack)
				sup.mutex.Lock()
				sup.errors = append(sup.errors, err)
				sup.wantsShutdown = true
				sup.mutex.Unlock()
			}
			close(done)
		}()

		err = fn()
	}()

	select {
	case <-p.Shutdown():
		return clean()
	case <-done:
		return
	}
}
