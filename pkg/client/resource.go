package client

import (
	"time"

	"github.com/datawire/ambassador/pkg/supervisor"
)

// Resource represents one thing managed by telepresence background processes. Examples include
// network intercepts (via teleproxy intercept) and cluster connectivity.
type Resource interface {
	Name() string
	IsOkay() bool
	Close() error
}

// ResourceBase has helpers to create a monitored resource
type ResourceBase struct {
	name    string
	doCheck func(*supervisor.Process) error
	doQuit  func(*supervisor.Process) error
	tasks   chan func(*supervisor.Process) error
	transAt time.Time     // (monitor) time of transition (okay value changed)
	end     chan struct{} // (Close) closed when the processor finishes
	okay    bool          // (monitor) cmd is running and check passes
	done    bool          // (Close) to get everything to quit
}

func (rb *ResourceBase) AddTask(task func(*supervisor.Process) error) {
	rb.tasks <- task
}

func (rb *ResourceBase) SetDone() {
	rb.done = true
}

// Name implements Resource
func (rb *ResourceBase) Name() string {
	return rb.name
}

// IsOkay returns whether the resource is okay as far as monitoring is aware
func (rb *ResourceBase) IsOkay() bool {
	res := make(chan bool)
	rb.tasks <- func(_ *supervisor.Process) error {
		res <- rb.okay
		return nil
	}
	return <-res
}

// Close shuts down this resource
func (rb *ResourceBase) Close() error {
	if rb.tasks != nil {
		rb.tasks <- rb.quit
		<-rb.end // Wait until things have closed
		rb.tasks = nil
	}
	return nil
}

func (rb *ResourceBase) Setup(sup *supervisor.Supervisor, name string, check, quit func(*supervisor.Process) error) {
	rb.name = name
	rb.doCheck = check
	rb.doQuit = quit
	rb.tasks = make(chan func(*supervisor.Process) error, 10)
	rb.end = make(chan struct{})
	rb.transAt = time.Now()
	sup.Supervise(&supervisor.Worker{
		Name: name,
		Work: rb.processor,
	})
	sup.Supervise(&supervisor.Worker{
		Name: name + "/shutdown",
		Work: func(p *supervisor.Process) error {
			select {
			case <-p.Shutdown():
				p.Logf("%s is shutting down", rb.name)
				return rb.Close()
			case <-rb.end:
				p.Log("Close() complete")
				return nil
			}
		},
	})
}

func (rb *ResourceBase) quit(p *supervisor.Process) error {
	p.Log("Close() / resource quit() called")
	return rb.doQuit(p)
}

func (rb *ResourceBase) monitor(p *supervisor.Process) error {
	old := rb.okay
	p.Log("monitor: checking...")
	if err := rb.doCheck(p); err != nil {
		rb.okay = false // Check failed is not okay
		p.Logf("monitor: check failed: %v", err)
	} else {
		p.Log("monitor: check passed")
		rb.okay = true
	}
	if old != rb.okay {
		rb.transAt = time.Now()
	}
	return nil
}

func (rb *ResourceBase) processor(p *supervisor.Process) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	defer close(rb.end)
	p.Ready()
	for {
		var task func(*supervisor.Process) error
		select {
		case fn := <-rb.tasks: // There is work to do
			task = fn
		case <-ticker.C: // Ticker says it's time to monitor
			task = rb.monitor
		}
		if err := task(p); err != nil {
			p.Logf("task failed: %v", err)
			return err
		}
		if rb.done {
			p.Log("done")
			return nil
		}
	}
}
