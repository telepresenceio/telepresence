package supervisor

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/pkg/errors"
)

// A supervisor provides an abstraction for managing a group of
// related goroutines, and provides:
//
// - startup and shutdown ordering based on dependencies
// - both graceful and hard shutdown
// - error propagation
// - retry
// - logging
//
type Supervisor struct {
	mutex         *sync.Mutex
	changed       *sync.Cond // used to signal when a worker is ready or done
	context       context.Context
	wantsShutdown bool               // signals we are in shutdown mode
	names         []string           // list of worker names in order added
	workers       map[string]*Worker // keyed by worker name
	errors        []error
	Logger        Logger
}

// centralize a bit of lock management
func (s *Supervisor) change(f func()) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	f()
	s.changed.Broadcast()
}

func WithContext(ctx context.Context) *Supervisor {
	mu := &sync.Mutex{}
	return &Supervisor{
		mutex:   mu,
		changed: sync.NewCond(mu),
		context: ctx,
		workers: make(map[string]*Worker),
		Logger:  &DefaultLogger{},
	}
}

// Supervise adds a Worker to be run as a Process when s.Run() is
// called.
func (s *Supervisor) Supervise(worker *Worker) {
	s.change(func() {
		_, exists := s.workers[worker.Name]
		if exists {
			panic(fmt.Sprintf("worker already exists: %s", worker.Name))
		}
		worker.supervisor = s
		s.add(worker)
	})
}

func (s *Supervisor) add(worker *Worker) {
	s.workers[worker.Name] = worker
	s.names = append(s.names, worker.Name)
}

// this assumes that s.mutex is already held
func (s *Supervisor) remove(worker *Worker) {
	delete(s.workers, worker.Name)
	var newNames []string
	for _, name := range s.names {
		if name == worker.Name {
			continue
		} else {
			newNames = append(newNames, name)
		}
	}
	s.names = newNames
}

// A supervisor will run until all its workers exit. There are
// multiple ways workers can exit:
//
//   - normally (returning a non-nil error)
//   - error (either via return result or panic)
//   - graceful shutdown request
//   - canceled context
//
// A normal exit does not trigger any special action other than
// causing Run to return if it is the last worker.
//
// If a worker exits with an error, the behavior depends on the value
// of the Retry flag on the worker. If Retry is true, the worker will
// be restarted. If not the supervisor shutdown sequence is triggerd.
//
// The supervisor shutdown sequence can be deliberately triggered by
// invoking supervisor.Shutdown(). This can be done from any goroutine
// including workers.
//
// The graceful shutdown sequence shuts down workers in an order that
// respects worker dependencies.
func (s *Supervisor) Run() []error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// we make cancel trigger shutdown so that simple cases only
	// need to worry about shutdown
	go func() {
		ticker := time.NewTicker(1 * time.Second)

		for {
			select {
			case <-s.context.Done():
				s.Shutdown()
				ticker.Stop()
				return
			case <-ticker.C:
				s.changed.Broadcast()
			}
		}
	}()

	// reconcile may delete workers
	s.reconcile()
	for len(s.workers) > 0 {
		s.changed.Wait()
		s.reconcile()
	}
	return s.errors
}

// Triggers a graceful shutdown sequence. This can be invoked from any
// goroutine.
func (s *Supervisor) Shutdown() {
	s.change(func() {
		s.wantsShutdown = true
	})
}

// Gets the worker with the specified name. Will return nil if no such
// worker exists.
func (s *Supervisor) Get(name string) *Worker {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.workers[name]
}

func (s *Supervisor) dependents(worker *Worker) (result []*Worker) {
	for _, n := range s.names {
		w := s.workers[n]
		for _, r := range w.Requires {
			if r == worker.Name {
				result = append(result, w)
				break
			}
		}
	}
	return
}

// make sure anything that would like to be running is actually
// running
func (s *Supervisor) reconcile() {
	var cleanup []string
	for _, n := range s.names {
		w := s.workers[n]
		remove := w.reconcile()
		if remove {
			cleanup = append(cleanup, w.Name)
		}
	}

	for _, n := range cleanup {
		w := s.workers[n]
		s.remove(w)
	}
}

func (s *Supervisor) launch(worker *Worker) {
	process := &Process{
		supervisor: s,
		worker:     worker,
		shutdown:   make(chan struct{}),
	}
	worker.process = process
	go func() {
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					stack := string(debug.Stack())
					err = errors.Errorf("WORKER PANICKED: %v\n%s", r, stack)
				}
			}()
			time.Sleep(worker.retryDelay)
			err = worker.Work(process)
		}()
		s.mutex.Lock()
		defer s.mutex.Unlock()
		worker.process = nil
		if err != nil {
			process.Logf("ERROR: %v", err)
			if worker.Retry {
				if worker.shuttingDown() {
					s.remove(worker)
					worker.done = true
				} else {
					worker.retryDelay = nextDelay(worker.retryDelay)
					process.Logf("retrying after %s...", worker.retryDelay.String())
				}
			} else {
				s.remove(worker)
				worker.error = err
				s.errors = append(s.errors, worker)
				s.wantsShutdown = true
				worker.done = true
			}
		} else {
			process.Logf("exited")
			s.remove(worker)
			worker.done = true
		}
		s.changed.Broadcast()
	}()
}
