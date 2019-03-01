package supervisor

import (
	"context"
	"fmt"
	"log"
	"sync"

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
	mutex        *sync.Mutex
	changed      *sync.Cond // used to signal when a worker is ready or done
	context      context.Context
	shuttingDown bool               // signals we are in shutdown mode
	names        []string           // list of worker names in order added
	workers      map[string]*Worker // keyed by worker name
	errors       []error
}

func WithContext(ctx context.Context) *Supervisor {
	mu := &sync.Mutex{}
	return &Supervisor{
		mutex:   mu,
		changed: sync.NewCond(mu),
		context: ctx,
		workers: make(map[string]*Worker),
	}
}

type Worker struct {
	Name     string               // the name of the worker
	Work     func(*Process) error // the function to perform the work
	Requires []string             // a list of required worker names
	Retry    bool                 // whether or not to retry on error
	process  *Process             // nil if the worker is not currently running
}

func (s *Supervisor) Supervise(worker *Worker) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	_, exists := s.workers[worker.Name]
	if exists {
		panic(fmt.Sprintf("worker already exists: %s", worker.Name))
	}
	s.workers[worker.Name] = worker
	s.names = append(s.names, worker.Name)
}

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
	for len(s.workers) > 0 {
		s.reconcile()
		s.changed.Wait()
	}
	return s.errors
}

// Triggers a graceful shutdown sequence. This can be invoked from any
// goroutine.
func (s *Supervisor) Shutdown() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.shuttingDown = true
	s.changed.Broadcast()
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
	if s.shuttingDown {
		s.reconcileShutdown()
	} else {
		s.reconcileNormal()
	}
}

// shutdown anything that is safe to shutdown and not already shutdown
func (s *Supervisor) reconcileShutdown() {
OUTER:
	for _, n := range s.names {
		w := s.workers[n]
		if w.process != nil && !w.process.shutdownClosed {
			for _, d := range s.dependents(w) {
				if s.workers[d.Name].process != nil {
					log.Printf("cannot shutdown %s, %s still running", n, d.Name)
					continue OUTER
				}
			}
			close(w.process.shutdown)
			w.process.shutdownClosed = true
		}
	}
}

// launch anything that is safe to launch and not already launched
func (s *Supervisor) reconcileNormal() {
OUTER:
	for _, n := range s.names {
		w := s.workers[n]
		if w.process == nil {
			for _, r := range w.Requires {
				process := s.workers[r].process
				if process == nil || !process.ready {
					log.Printf("cannot start %s, %s not ready", n, r)
					continue OUTER
				}
			}
			s.launch(w)
		}
	}
}

func (s *Supervisor) launch(worker *Worker) {
	context, cancel := context.WithCancel(s.context)
	process := &Process{
		Supervisor: s,
		Worker:     worker,
		Context:    context,
		cancel:     cancel,
		shutdown:   make(chan struct{}),
	}
	worker.process = process
	go func() {
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = errors.Errorf("PANIC: %v", r)
				}
			}()
			err = worker.Work(process)
		}()
		s.mutex.Lock()
		defer s.mutex.Unlock()
		worker.process = nil
		if err != nil {
			process.Log(err)
			if worker.Retry {
				if s.shuttingDown {
					s.remove(worker)
				} else {
					process.Log("retrying...")
				}
			} else {
				s.remove(worker)
				s.errors = append(s.errors, err)
				s.shuttingDown = true
			}
		} else {
			s.remove(worker)
		}
		s.changed.Broadcast()
	}()
}

type Process struct {
	Supervisor *Supervisor
	Worker     *Worker
	// Used for hard cancel.
	Context context.Context
	cancel  context.CancelFunc
	// Used to signal graceful shutdown.
	shutdown       chan struct{}
	ready          bool
	shutdownClosed bool
}

// Invoked by a worker to signal it is ready.
func (p *Process) Ready() {
	p.Supervisor.mutex.Lock()
	defer p.Supervisor.mutex.Unlock()
	p.ready = true
	p.Supervisor.changed.Broadcast()
}

// Used for graceful shutdown...
func (p *Process) Shutdown() <-chan struct{} {
	return p.shutdown
}

// Used for logging...
func (p *Process) Log(obj interface{}) {
	log.Printf("%s: %v", p.Worker.Name, obj)
}

func (p *Process) Logf(format string, args ...interface{}) {
	log.Printf("%s: %v", p.Worker.Name, fmt.Sprintf(format, args...))
}
