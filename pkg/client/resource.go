package client

import (
	"context"
	"time"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
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
	doCheck func(context.Context) error
	doQuit  func(context.Context) error
	tasks   chan func(context.Context) error
	transAt time.Time     // (monitor) time of transition (okay value changed)
	end     chan struct{} // (Close) closed when the processor finishes
	okay    bool          // (monitor) cmd is running and check passes
	done    bool          // (Close) to get everything to quit
}

// AddTask adds a taskt to be performed by the resource to its task queue.
func (rb *ResourceBase) AddTask(task func(context.Context) error) {
	rb.tasks <- task
}

// SetDone declares this resource as done. This will end the loop.
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
	rb.tasks <- func(_ context.Context) error {
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

// Setup initializes this resource
func (rb *ResourceBase) Setup(c context.Context, name string, check, quit func(context.Context) error) {
	rb.name = name
	rb.doCheck = check
	rb.doQuit = quit
	rb.tasks = make(chan func(context.Context) error, 10)
	rb.end = make(chan struct{})
	rb.transAt = time.Now()
	pg := dgroup.ParentGroup(c)
	pg.Go(name, rb.processor)
	pg.Go(name+"/shutdown", func(context.Context) error {
		select {
		case <-c.Done():
			dlog.Infof(dcontext.HardContext(c), "%s is shutting down", rb.name)
			return rb.Close()
		case <-rb.end:
			dlog.Info(c, "Close() complete")
			return nil
		}
	})
}

func (rb *ResourceBase) quit(c context.Context) error {
	dlog.Debug(c, "Close() / resource quit() called")
	return rb.doQuit(c)
}

func (rb *ResourceBase) monitor(c context.Context) error {
	old := rb.okay
	if err := rb.doCheck(c); err != nil {
		rb.okay = false // Check failed is not okay
	} else {
		rb.okay = true
	}
	if old != rb.okay {
		rb.transAt = time.Now()
	}
	return nil
}

func (rb *ResourceBase) processor(c context.Context) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	defer close(rb.end)
	for {
		var task func(context.Context) error
		select {
		case fn := <-rb.tasks: // There is work to do
			task = fn
		case <-ticker.C: // Ticker says it's time to monitor
			task = rb.monitor
		}
		if err := task(c); err != nil {
			dlog.Errorf(c, "task failed: %v", err)
			return err
		}
		if rb.done {
			dlog.Debug(c, "done")
			return nil
		}
	}
}
