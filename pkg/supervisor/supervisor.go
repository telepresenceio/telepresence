package supervisor

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/pkg/errors"
)

type Logger interface {
	Printf(format string, v ...interface{})
}

type DefaultLogger struct{}

func (d *DefaultLogger) Printf(format string, v ...interface{}) {
	log.Printf(format, v...)
}

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

type Worker struct {
	Name          string               // the name of the worker
	Work          func(*Process) error // the function to perform the work
	Requires      []string             // a list of required worker names
	Retry         bool                 // whether or not to retry on error
	wantsShutdown bool                 // true if the worker wants to shut down
	supervisor    *Supervisor          //
	children      int64                // atomic counter for naming children
	process       *Process             // nil if the worker is not currently running
	error         error
}

func (w *Worker) Error() string {
	return fmt.Sprintf("%s: %s", w.Name, w.error.Error())
}

func (s *Supervisor) Supervise(worker *Worker) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	_, exists := s.workers[worker.Name]
	if exists {
		panic(fmt.Sprintf("worker already exists: %s", worker.Name))
	}
	s.workers[worker.Name] = worker
	worker.supervisor = s
	s.names = append(s.names, worker.Name)
	s.changed.Broadcast()
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
		<-s.context.Done()
		s.Shutdown()
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
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.wantsShutdown = true
	s.changed.Broadcast()
}

// Gets the worker with the specified name. Will return nil if no such
// worker exists.
func (s *Supervisor) Get(name string) *Worker {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.workers[name]
}

// Shuts down the worker. Note that if the worker has other workers
// that depend on it, the shutdown won't actually be initiated until
// those dependent workers exit.
func (w *Worker) Shutdown() {
	s := w.supervisor
	s.mutex.Lock()
	defer s.mutex.Unlock()
	w.wantsShutdown = true
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
					s.Logger.Printf("cannot shutdown %s, %s still running", w.Name, d.Name)
					return false
				}
			}
			s.Logger.Printf("shutting down %s", w.Name)
			close(w.process.shutdown)
			w.process.shutdownClosed = true
		}
		if w.process == nil {
			return true
		}
	} else if true { // I really just wanted an else here, but lint wouldn't let me do that.
		if w.process == nil {
			for _, r := range w.Requires {
				required := s.workers[r]
				if required == nil {
					s.Logger.Printf("cannot start %s, required worker missing: %s", w.Name, r)
					return false
				}
				process := required.process
				if process == nil || !process.ready {
					s.Logger.Printf("cannot start %s, %s not ready", w.Name, r)
					return false
				}
			}
			s.Logger.Printf("starting %s", w.Name)
			s.launch(w)
		}

	}
	return false
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
				if worker.shuttingDown() {
					s.remove(worker)
				} else {
					process.Log("retrying...")
				}
			} else {
				s.remove(worker)
				worker.error = err
				s.errors = append(s.errors, worker)
				s.wantsShutdown = true
			}
		} else {
			s.remove(worker)
		}
		s.changed.Broadcast()
	}()
}

type Process struct {
	supervisor *Supervisor
	worker     *Worker
	// Used to signal graceful shutdown.
	shutdown       chan struct{}
	ready          bool
	shutdownClosed bool
}

func (p *Process) Supervisor() *Supervisor {
	return p.supervisor
}

func (p *Process) Worker() *Worker {
	return p.worker
}

func (p *Process) Context() context.Context {
	return p.supervisor.context
}

// Invoked by a worker to signal it is ready.
func (p *Process) Ready() {
	p.Supervisor().mutex.Lock()
	defer p.Supervisor().mutex.Unlock()
	p.ready = true
	p.Supervisor().changed.Broadcast()
}

// Used for graceful shutdown...
func (p *Process) Shutdown() <-chan struct{} {
	return p.shutdown
}

// Used for logging...
func (p *Process) Log(obj interface{}) {
	p.supervisor.Logger.Printf("%s: %v", p.Worker().Name, obj)
}

func (p *Process) Logf(format string, args ...interface{}) {
	p.supervisor.Logger.Printf("%s: %v", p.Worker().Name, fmt.Sprintf(format, args...))
}

func (p *Process) Go(fn func(*Process) error) *Worker {
	id := atomic.AddInt64(&p.Worker().children, 1)
	w := &Worker{
		Name: fmt.Sprintf("%s[%d]", p.Worker().Name, id),
		Work: fn,
	}
	p.Supervisor().Supervise(w)
	return w
}
