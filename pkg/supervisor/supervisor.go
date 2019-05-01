package supervisor

import (
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"runtime/debug"
	"strings"
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

func Run(name string, f func(*Process) error) []error {
	sup := WithContext(context.Background())
	sup.Supervise(&Worker{Name: name, Work: f})
	return sup.Run()
}

func MustRun(name string, f func(*Process) error) {
	errs := Run(name, f)
	if len(errs) > 0 {
		panic(fmt.Sprintf("%s: %v", name, errs))
	}
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
	done          bool
	supervisor    *Supervisor //
	children      int64       // atomic counter for naming children
	process       *Process    // nil if the worker is not currently running
	error         error
}

func (w *Worker) Error() string {
	if w.error == nil {
		return "worker without an error"
	} else {
		return fmt.Sprintf("%s: %s", w.Name, w.error.Error())
	}
}

func (w *Worker) Wait() {
	s := w.supervisor
	s.mutex.Lock()
	defer s.mutex.Unlock()

	for !w.done {
		s.changed.Wait()
	}
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

	// XXX: added this for debugging shutdown stalls, need a better way to
	// log this that doesn't add so much noise normally
	//
	//s.Logger.Printf("WORKERS: %v", s.names)

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
					return false
				}
				process := required.process
				if process == nil || !process.ready {
					return false
				}
			}
			s.Logger.Printf("%s: starting", w.Name)
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
					stack := string(debug.Stack())
					err = errors.Errorf("WORKER PANICKED: %v\n%s", r, stack)
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
					worker.done = true
				} else {
					process.Log("retrying...")
				}
			} else {
				s.remove(worker)
				worker.error = err
				s.errors = append(s.errors, worker)
				s.wantsShutdown = true
				worker.done = true
			}
		} else {
			s.remove(worker)
			worker.done = true
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

func (p *Process) allocateId() int64 {
	return atomic.AddInt64(&p.Worker().children, 1)
}

// Shorthand for launching a child worker... it is named "<parent>[<child-count>]"
func (p *Process) Go(fn func(*Process) error) *Worker {
	w := &Worker{
		Name: fmt.Sprintf("%s[%d]", p.Worker().Name, p.allocateId()),
		Work: fn,
	}
	p.Supervisor().Supervise(w)
	return w
}

// Shorthand for proper shutdown handling while doing a potentially
// blocking activity. This method will return true if the activity
// completes normally and false if it was abandoned.
func (p *Process) Do(fn func()) bool {
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()

	select {
	case <-p.Shutdown():
		return false
	case <-done:
		return true
	}
}

type logger struct {
	process   *Process
	emptyLine bool
}

func (l *logger) Log(prefix, line string) {
	if l.emptyLine {
		l.process.Log(prefix)
		l.emptyLine = false
	}

	if line == "" {
		l.emptyLine = true
	} else {
		l.process.Logf("%s%s", prefix, line)
		l.emptyLine = false
	}
}

func (l *logger) LogLines(prefix, str string, err error) {
	lines := strings.Split(str, "\n")
	for _, line := range lines {
		l.Log(prefix, line)
	}

	if !(err == nil || err == io.EOF) {
		l.process.Log(fmt.Sprintf("%v", err))
	}
}

type loggingWriter struct {
	logger
	writer io.Writer
}

func (l *loggingWriter) Write(bytes []byte) (int, error) {
	if l.writer == nil {
		l.LogLines(" <- ", string(bytes), nil)
		return len(bytes), nil
	} else {
		n, err := l.writer.Write(bytes)
		l.LogLines(" <- ", string(bytes[:n]), err)
		return n, err
	}
}

type loggingReader struct {
	logger
	reader io.Reader
}

func (l *loggingReader) Read(p []byte) (n int, err error) {
	n, err = l.reader.Read(p)
	l.LogLines(" -> ", string(p[:n]), err)
	return n, err
}

type Cmd struct {
	*exec.Cmd
	supervisorProcess *Process
}

func (c *Cmd) pre() {
	if c.Stdin != nil {
		c.Stdin = &loggingReader{logger: logger{process: c.supervisorProcess}, reader: c.Stdin}
	}
	c.Stdout = &loggingWriter{logger: logger{process: c.supervisorProcess}, writer: c.Stdout}
	c.Stderr = &loggingWriter{logger: logger{process: c.supervisorProcess}, writer: c.Stderr}

	c.supervisorProcess.Logf("%s %v", c.Path, c.Args[1:])
}

func (c *Cmd) post(err error) {
	if err == nil {
		c.supervisorProcess.Logf("%s exited successfully", c.Path)
	} else {
		if c.ProcessState == nil {
			c.supervisorProcess.Logf("%v", err)
		} else {
			c.supervisorProcess.Logf("%s: %v", c.Path, err)
		}
	}
}

func (c *Cmd) Start() error {
	c.pre()
	return c.Cmd.Start()
}

func (c *Cmd) Wait() error {
	err := c.Cmd.Wait()
	c.post(err)
	return err
}

func (c *Cmd) Run() error {
	c.pre()
	err := c.Cmd.Run()
	c.post(err)
	return err
}

// Creates a command that automatically logs inputs, outputs, and exit
// codes to the process logger.
func (p *Process) Command(name string, args ...string) *Cmd {
	return &Cmd{exec.Command(name, args...), p}
}

// Runs a command with the supplied input and captures the output as a
// string.
func (c *Cmd) Capture(stdin io.Reader) (output string, err error) {
	c.Stdin = stdin
	out := strings.Builder{}
	c.Stdout = &out
	err = c.Run()
	output = out.String()
	return
}

func (c *Cmd) MustCapture(stdin io.Reader) (output string) {
	output, err := c.Capture(stdin)
	if err != nil {
		panic(err)
	}
	return output
}
