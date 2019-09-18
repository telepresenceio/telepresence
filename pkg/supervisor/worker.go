package supervisor

import (
	"fmt"
	"time"
)

// A Worker represents a managed goroutine being prepared or run.
//
// I (LukeShu) don't think a Worker can be reused after being run by a
// Supervisor.
type Worker struct {
	Name               string               // the name of the worker
	Work               func(*Process) error // the function to perform the work
	Requires           []string             // a list of required worker names
	Retry              bool                 // whether or not to retry on error
	wantsShutdown      bool                 // true if the worker wants to shut down
	done               bool
	supervisor         *Supervisor //
	children           int64       // atomic counter for naming children
	process            *Process    // nil if the worker is not currently running
	error              error
	retryDelay         time.Duration // how long to wait to retry
	lastBlockedWarning time.Time     // last time we warned about being blocked
}

func (w *Worker) Error() string {
	if w.error == nil {
		return "worker without an error"
	}
	return fmt.Sprintf("%s: %s", w.Name, w.error.Error())
}

func (w *Worker) reset() {
	w.wantsShutdown = false
	w.done = false
	w.error = nil
	w.lastBlockedWarning = time.Time{}
}

// Restart is used to cause a finished Worker to restart. It can only
// be called on Workers that are done. The only way to be sure a
// worker is done is to call Wait() on it, e.g.:
//
//     ...
//     worker.Shutdown()
//     worker.Wait()
//     worker.Restart()
//     ...
func (w *Worker) Restart() {
	s := w.supervisor
	s.change(func() {
		w.reset()
		s.add(w)
	})
}

// Wait blocks until the worker is done.
func (w *Worker) Wait() {
	s := w.supervisor
	s.mutex.Lock()
	defer s.mutex.Unlock()

	for !w.done {
		s.changed.Wait()
	}
}

func (w *Worker) shuttingDown() bool {
	return w.wantsShutdown || w.supervisor.wantsShutdown
}

// returns true if the worker is done and should be removed from the supervisor
func (w *Worker) reconcile() bool {
	s := w.supervisor
	if w.shuttingDown() {
		if w.process != nil && !w.process.shutdownClosed {
			for _, d := range s.dependents(w) {
				if s.workers[d.Name].process != nil {
					return false
				}
			}
			s.Logger.Printf("%s: signaling shutdown", w.Name)
			close(w.process.shutdown)
			w.process.shutdownClosed = true
		}
		if w.process == nil {
			if !w.done {
				w.done = true
				s.changed.Broadcast()
			}
			return true
		}
	} else if true { // I really just wanted an else here, but lint wouldn't let me do that.
		if w.process == nil {
			for _, r := range w.Requires {
				required := s.workers[r]
				if required == nil {
					w.maybeWarnBlocked(r, "not created")
					return false
				}
				process := required.process
				if process == nil {
					w.maybeWarnBlocked(r, "not started")
					return false
				}
				if !process.ready {
					w.maybeWarnBlocked(r, "not ready")
					return false
				}
			}
			s.Logger.Printf("%s: starting", w.Name)
			s.launch(w)
		}

	}
	return false
}

func (w *Worker) maybeWarnBlocked(name, cond string) {
	now := time.Now()
	if w.lastBlockedWarning == (time.Time{}) {
		w.lastBlockedWarning = now
		return
	}

	if now.Sub(w.lastBlockedWarning) > 3*time.Second {
		w.supervisor.Logger.Printf("%s: blocked on %s (%s)", w.Name, name, cond)
		w.lastBlockedWarning = now
	}
}

// Shutdown shuts down the worker. Note that if the worker has other
// workers that depend on it, the shutdown won't actually be initiated
// until those dependent workers exit.
func (w *Worker) Shutdown() {
	s := w.supervisor
	s.change(func() {
		w.wantsShutdown = true
	})
}
