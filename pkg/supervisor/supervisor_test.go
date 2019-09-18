package supervisor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	UNSTARTED = 0
	SETUP     = 1
	READY     = 2
	SHUTDOWN  = 3
	CANCEL    = 4
	EXITING   = 5
)

type Root map[string]*Spec

type Spec struct {
	Name       string
	Error      string
	Requires   []string
	Retry      bool
	OnStartup  func(*Spec)
	OnReady    func(*Spec)
	OnShutdown func(*Spec)
	OnCancel   func(*Spec)
	process    *Process
	// holds the state of the worker
	state  int
	result error
	// map of all the other workers
	coworkers map[string]*Spec
}

func (spec *Spec) wait(timeout time.Duration) {
	var timeoutChan <-chan time.Time
	if timeout < 0 {
		timeoutChan = make(chan time.Time)
	} else {
		timeoutChan = time.After(timeout)
	}
	p := spec.process
	select {
	case <-p.Shutdown():
		spec.state = SHUTDOWN
		p.Log("shutting down...")
		if spec.OnShutdown != nil {
			spec.OnShutdown(spec)
		}
		p.Log("shut down")
	case <-p.Context().Done():
		spec.state = CANCEL
		p.Log("hard shutdown")
		if spec.OnCancel != nil {
			spec.OnCancel(spec)
		}
	case <-timeoutChan:
		p.Log("shutting down spontaneously")
	}
}

func (spec *Spec) assertRequiredReady() {
	for _, r := range spec.Requires {
		rs := spec.coworkers[r]
		if rs.state != READY {
			panic(fmt.Sprintf("%s is not ready", r))
		}
	}
}

func (r Root) worker(spec Spec) *Worker {
	r[spec.Name] = &spec
	spec.state = UNSTARTED
	spec.coworkers = r
	return &Worker{
		Name: spec.Name,
		Work: func(p *Process) error {
			spec.process = p
			spec.state = SETUP
			p.Log("setting up...")
			spec.assertRequiredReady()
			if spec.OnStartup != nil {
				spec.OnStartup(&spec)
			}

			spec.state = READY
			p.Log("ready")
			p.Ready()
			if spec.OnReady != nil {
				spec.OnReady(&spec)
			}

			spec.state = EXITING
			if spec.Error == "" {
				p.Log("exiting normally")
			} else {
				p.Logf("exiting with error: %s", spec.Error)
				spec.result = fmt.Errorf(spec.Error)
			}
			return spec.result
		},
		Requires: spec.Requires,
		Retry:    spec.Retry,
	}
}

func newRoot() Root {
	return make(map[string]*Spec)
}

func TestNormalExit(t *testing.T) {
	r := newRoot()
	s := WithContext(context.Background())
	N := 3
	counts := make([]int, N)
	for i := 0; i < N; i++ {
		num := i
		s.Supervise(r.worker(Spec{
			Name: fmt.Sprintf("minion-%d", num),
			OnReady: func(spec *Spec) {
				counts[num]++
			},
		}))
	}
	errors := s.Run()
	for idx, count := range counts {
		if count != 1 {
			t.Errorf("minion %d failed to increment count", idx)
		}
	}
	if len(errors) != 0 {
		t.Errorf("unexpected errors: %v", errors)
	}
}

func TestErrorExit(t *testing.T) {
	r := newRoot()
	s := WithContext(context.Background())
	N := 3
	counts := make([]int, N)
	for i := 0; i < N; i++ {
		num := i
		s.Supervise(r.worker(Spec{
			Name: fmt.Sprintf("minion-%d", num),
			OnReady: func(spec *Spec) {
				counts[num]++
			},
			Error: fmt.Sprintf("boo-%d", num),
		}))
	}
	errors := s.Run()
	for i, count := range counts {
		if count != 1 {
			t.Errorf("unexpected count %d: %d", i, count)
		}
	}
	if len(errors) != N {
		t.Fail()
	}
	for _, err := range errors {
		wrk := err.(*Worker)
		if !strings.HasPrefix(err.Error(), fmt.Sprintf("%s: boo-", wrk.Name)) {
			t.Fail()
		}
	}
}

func TestPanicExit(t *testing.T) {
	r := newRoot()
	s := WithContext(context.Background())
	N := 3
	counts := make([]int, N)
	for i := 0; i < N; i++ {
		num := i
		s.Supervise(r.worker(Spec{
			Name: fmt.Sprintf("minion-%d", num),
			OnReady: func(spec *Spec) {
				counts[num]++
				panic(fmt.Sprintf("boo-%d", num))
			},
		}))
	}
	errors := s.Run()
	for i, count := range counts {
		if count != 1 {
			t.Errorf("unexpected count %d: %d", i, count)
		}
	}
	if len(errors) != N {
		t.Fail()
	}
	for _, err := range errors {
		wrk := err.(*Worker)
		if !strings.HasPrefix(err.Error(), fmt.Sprintf("%s: WORKER PANICKED: boo-", wrk.Name)) {
			t.Errorf("unexpected error: %v", err)
		}
	}
}

func TestDependency(t *testing.T) {
	r := newRoot()
	s := WithContext(context.Background())
	s.Supervise(r.worker(Spec{
		Name: "minion",
		OnShutdown: func(spec *Spec) {
			if r["dependent-minion"].state != EXITING {
				panic("dependent-minion has not exited")
			}
		},
		OnReady: func(spec *Spec) { spec.wait(-1) },
	}))
	s.Supervise(r.worker(Spec{
		Name:     "dependent-minion",
		Requires: []string{"minion"},
		OnReady: func(spec *Spec) {
			if r["minion"].state != READY {
				panic("minion not ready")
			}
			spec.process.Supervisor().Shutdown()
		},
	}))
	errors := s.Run()
	if len(errors) != 0 {
		t.Errorf("unexpected errors: %v", errors)
	}
}

func TestDependencyPanic(t *testing.T) {
	r := newRoot()
	s := WithContext(context.Background())
	s.Supervise(r.worker(Spec{
		Name: "minion",
		OnStartup: func(spec *Spec) {
			panic("oops")
		},
		OnReady: func(spec *Spec) { spec.wait(-1) },
	}))
	s.Supervise(r.worker(Spec{
		Name:     "dependent-minion",
		Requires: []string{"minion"},
	}))
	errors := s.Run()
	if !(len(errors) == 1 && strings.HasPrefix(errors[0].Error(), "minion: WORKER PANICKED: oops")) {
		t.Errorf("unexpected errors: %v", errors)
	}
	if r["dependent-minion"].state != UNSTARTED {
		t.Errorf("dependent-minion was started")
	}
}

func TestShutdownOnError(t *testing.T) {
	r := newRoot()
	s := WithContext(context.Background())
	s.Supervise(r.worker(Spec{
		Name:    "forever",
		OnReady: func(spec *Spec) { spec.wait(-1) },
	}))
	s.Supervise(r.worker(Spec{
		Name:  "buggy",
		Error: "bug",
	}))
	errors := s.Run()
	if !(len(errors) == 1 && errors[0].Error() == "buggy: bug") {
		t.Errorf("unexpected errors: %v", errors)
	}
}

func TestShutdownOnPanic(t *testing.T) {
	r := newRoot()
	s := WithContext(context.Background())
	s.Supervise(r.worker(Spec{
		Name:    "forever",
		OnReady: func(spec *Spec) { spec.wait(-1) },
	}))
	s.Supervise(r.worker(Spec{
		Name:    "buggy",
		OnReady: func(spec *Spec) { panic("bug") },
	}))
	errors := s.Run()
	if !(len(errors) == 1 && strings.HasPrefix(errors[0].Error(), "buggy: WORKER PANICKED: bug")) {
		t.Errorf("unexpected errors: %v", errors)
	}
}

func TestShutdown(t *testing.T) {
	r := newRoot()
	s := WithContext(context.Background())
	s.Supervise(r.worker(Spec{
		Name:    "forever",
		OnReady: func(spec *Spec) { spec.wait(-1) },
	}))
	s.Supervise(r.worker(Spec{
		Name: "quitter",
		OnReady: func(spec *Spec) {
			spec.process.Supervisor().Shutdown()
			spec.wait(-1)
		},
	}))
	errors := s.Run()
	if len(errors) != 0 {
		t.Errorf("unexpected errors: %v", errors)
	}
}

// This test is probably written to be more specific than it
// necessarily needs to be... it's actually checking that we run the
// worker and then cancel, whereas if cancel happens prior to Run()
// being called, I'm guessing we might want to turn Run into a noop.
func TestCancelPreRun(t *testing.T) {
	r := newRoot()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := WithContext(ctx)
	ran := false
	s.Supervise(r.worker(Spec{
		Name: "forever",
		OnReady: func(spec *Spec) {
			ran = true
			spec.wait(-1)
		},
	}))
	errors := s.Run()
	if len(errors) != 0 {
		t.Errorf("unexpected errors: %v", errors)
	}
	if !ran {
		t.Fail()
	}
}

func TestCancelPostRun(t *testing.T) {
	r := newRoot()
	ctx, cancel := context.WithCancel(context.Background())
	s := WithContext(ctx)
	ran := false
	s.Supervise(r.worker(Spec{
		Name: "forever",
		OnStartup: func(spec *Spec) {
			ran = true
		},
		OnReady: func(spec *Spec) {
			spec.wait(-1)
		},
	}))
	s.Supervise(r.worker(Spec{
		Name:     "canceller",
		Requires: []string{"forever"},
		OnReady: func(spec *Spec) {
			if !ran {
				t.Fail()
			}
			cancel()
			spec.wait(-1)
		},
	}))

	errors := s.Run()
	if len(errors) != 0 {
		t.Errorf("unexpected errors: %v", errors)
	}
	if !ran {
		t.Fail()
	}
}

func TestCancelTriggersShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := WithContext(ctx)
	ran := false
	s.Supervise(&Worker{
		Name: "forever",
		Work: func(p *Process) error {
			cancel()
			<-p.Shutdown()
			ran = true
			return nil
		},
	})

	errors := s.Run()
	if len(errors) != 0 {
		t.Errorf("unexpected errors: %v", errors)
	}
	if !ran {
		t.Fail()
	}
}

func TestRetry(t *testing.T) {
	r := newRoot()
	s := WithContext(context.Background())
	N := 3
	count := 0
	s.Supervise(r.worker(Spec{
		Name:  "buggy",
		Error: "oops",
		Retry: true,
		OnReady: func(spec *Spec) {
			count += 1
			if count == N {
				spec.process.Supervisor().Shutdown()
			}
		},
	}))
	errors := s.Run()
	if len(errors) != 0 {
		t.Errorf("unexpected errors: %v", errors)
	}
	if count != N {
		t.Errorf("unexpected count: %d", count)
	}
}

func TestGo(t *testing.T) {
	r := newRoot()
	s := WithContext(context.Background())
	went := false
	s.Supervise(r.worker(Spec{
		Name: "entry",
		OnReady: func(spec *Spec) {
			spec.process.Go(func(p *Process) error {
				went = true
				return nil
			})
		},
	}))
	errors := s.Run()
	if len(errors) != 0 {
		t.Errorf("unexpected errors: %v", errors)
	}
	if !went {
		t.Fail()
	}
}

func TestGoError(t *testing.T) {
	r := newRoot()
	s := WithContext(context.Background())
	went := false
	s.Supervise(r.worker(Spec{
		Name: "entry",
		OnReady: func(spec *Spec) {
			spec.process.Go(func(p *Process) error {
				went = true
				return fmt.Errorf("boo")
			})
		},
	}))
	errors := s.Run()
	if !(len(errors) == 1 && errors[0].Error() == "entry[1]: boo") {
		t.Errorf("unexpected errors: %v", errors)
	}
	if !went {
		t.Fail()
	}
}

func TestGoPanic(t *testing.T) {
	r := newRoot()
	s := WithContext(context.Background())
	went := false
	s.Supervise(r.worker(Spec{
		Name: "entry",
		OnReady: func(spec *Spec) {
			spec.process.Go(func(p *Process) error {
				went = true
				if true {
					panic("boo")
				}
				return nil
			})
		},
	}))
	errors := s.Run()
	if !(len(errors) == 1 && strings.HasPrefix(errors[0].Error(), "entry[1]: WORKER PANICKED: boo")) {
		t.Errorf("unexpected errors: %v", errors)
	}
	if !went {
		t.Fail()
	}
}

func TestWorkerShutdown(t *testing.T) {
	r := newRoot()
	s := WithContext(context.Background())
	fooRan := false
	s.Supervise(r.worker(Spec{
		Name: "foo",
		OnReady: func(spec *Spec) {
			fooRan = true
			// wait until we are shutdown
			<-spec.process.Shutdown()
		},
	}))
	s.Supervise(r.worker(Spec{
		Name:     "bar",
		Requires: []string{"foo"},
		OnReady: func(spec *Spec) {
			s.Get("foo").Shutdown()
		},
	}))
	errors := s.Run()
	if len(errors) != 0 {
		t.Errorf("unexpected errors: %v", errors)
	}
	if !fooRan {
		t.Fail()
	}
}

func TestSuperviseAfterRun(t *testing.T) {
	s := WithContext(context.Background())
	running := make(chan struct{})
	ran := false
	go func() {
		// wait until the supervisor is running
		<-running
		s.Supervise(&Worker{
			Name: "late",
			Work: func(p *Process) error {
				s.Shutdown()
				ran = true
				return nil
			},
		})
	}()
	s.Supervise(&Worker{
		Name: "forever",
		Work: func(p *Process) error {
			close(running)
			<-p.Shutdown()
			return nil
		},
	})
	errors := s.Run()
	if len(errors) != 0 {
		t.Errorf("unexpected errors: %v", errors)
	}
	if !ran {
		t.Fail()
	}
}

func TestWorkerWait(t *testing.T) {
	s := WithContext(context.Background())
	exit := make(chan struct{})
	log := make(chan string)
	w := &Worker{
		Name: "doit",
		Work: func(p *Process) error {
			<-exit
			log <- "exiting"
			return nil
		},
	}
	s.Supervise(w)

	go func() {
		s.Run()
	}()

	go func() {
		w.Wait()
		log <- "waited"
	}()

	close(exit)
	first := <-log
	second := <-log

	if first != "exiting" && second != "waited" {
		t.Errorf("first=%s, second=%s", first, second)
	}
}

func TestWaitOnWorkerStartedAfterShutdown(t *testing.T) {
	s := WithContext(context.Background())
	w := &Worker{Name: "noop", Work: func(p *Process) error { return nil }}
	s.Supervise(w)
	s.Shutdown()
	s.Run()
	w.Wait()
}

func TestDoPanic(t *testing.T) {
	gotHere := false
	errs := Run("bob", func(p *Process) error {
		p.Do(func() error {
			if true {
				panic("blah")
			}
			return nil
		})
		gotHere = true
		return nil
	})
	if len(errs) != 1 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if !gotHere {
		t.Errorf("did not recover from panic")
	}
}

func TestRestart(t *testing.T) {
	MustRun("sysiphus", func(p *Process) error {
		pipe := make(chan bool)
		w := &Worker{
			Name: "bob",
			Work: func(p *Process) error {
				p.Ready()
				pipe <- true
				<-p.Shutdown()
				return nil
			},
		}

		p.Supervisor().Supervise(w)

		<-pipe
		w.Shutdown()
		w.Wait()

		w.Restart()
		<-pipe
		w.Shutdown()
		w.Wait()

		return nil
	})
}
