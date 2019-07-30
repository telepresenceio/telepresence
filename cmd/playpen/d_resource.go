package main

import (
	"fmt"
	"strings"
	"syscall"
	"time"

	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/pkg/errors"
)

// Resource represents one thing managed by playpen daemon. Examples include
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
	doQuit  func() error
	tasks   chan func() error
	okay    bool          // (monitor) cmd is running and check passes
	done    bool          // (Close) to get everything to quit
	end     chan struct{} // (Close) closed when the processor finishes
}

// Name implements Resource
func (rb *ResourceBase) Name() string {
	res := make(chan string)
	rb.tasks <- func() error {
		res <- rb.name
		return nil
	}
	return <-res
}

// IsOkay returns whether the resource is okay as far as monitoring is aware
func (rb *ResourceBase) IsOkay() bool {
	res := make(chan bool)
	rb.tasks <- func() error {
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

func (rb *ResourceBase) setup(sup *supervisor.Supervisor, name string) {
	rb.name = name
	rb.tasks = make(chan func() error, 1)
	sup.Supervise(&supervisor.Worker{
		Name: name,
		Work: rb.processor,
	})
	sup.Supervise(&supervisor.Worker{
		Name: name + "/shutdown",
		Work: func(p *supervisor.Process) error {
			select {
			case <-p.Shutdown():
				p.Log("daemon is shutting down")
				return rb.Close()
			case <-rb.end:
				p.Log("Close() complete")
				return nil
			}
		},
	})
}

func (rb *ResourceBase) quit() error {
	return rb.doQuit()
}

func (rb *ResourceBase) monitor(p *supervisor.Process) error {
	old := rb.okay
	if err := rb.doCheck(p); err != nil {
		rb.okay = false // Check failed is not okay
		p.Logf("check failed: %v", err)
	} else {
		rb.okay = true
	}
	MaybeNotify(p, rb.name, old, rb.okay)
	return nil
}

func (rb *ResourceBase) processor(p *supervisor.Process) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	rb.end = make(chan struct{})
	defer close(rb.end)
	p.Ready()
	for {
		var task func() error
		select {
		case fn := <-rb.tasks: // There is work to do
			task = fn
		case <-ticker.C: // Ticker says it's time to monitor
			task = func() error { return rb.monitor(p) }
		}
		if err := task(); err != nil {
			p.Logf("task failed: %v", err)
			return err
		}
		if rb.done {
			p.Log("done")
			return nil
		}
	}
}

// KCluster is a Kubernetes cluster reference
type KCluster struct {
	context      string
	server       string
	rai          *RunAsInfo
	kargs        []string
	isBridgeOkay func() bool
	ResourceBase
}

// GetKubectlCmd returns a Cmd that runs kubectl with the given arguments and
// the appropriate environment to talk to the cluster
func (c *KCluster) GetKubectlCmd(p *supervisor.Process, args ...string) *supervisor.Cmd {
	cmdArgs := make([]string, 0, 1+len(c.kargs)+len(args))
	cmdArgs = append(cmdArgs, "kubectl")
	cmdArgs = append(cmdArgs, c.kargs...)
	cmdArgs = append(cmdArgs, args...)
	return c.rai.Command(p, cmdArgs...)
}

// Context returns the cluster's context name
func (c *KCluster) Context() string {
	return c.context
}

// Server returns the cluster's server configuration
func (c *KCluster) Server() string {
	return c.server
}

// SetBridgeCheck sets the callable used to check whether the Teleproxy bridge
// is functioning. If this is nil/unset, cluster monitoring checks the cluster
// directly (via kubectl)
func (c *KCluster) SetBridgeCheck(isBridgeOkay func() bool) {
	c.isBridgeOkay = isBridgeOkay
}

// check for cluster connectivity
func (c *KCluster) check(p *supervisor.Process) error {
	// If the bridge is okay then the cluster is okay
	if c.isBridgeOkay != nil && c.isBridgeOkay() {
		return nil
	}
	cmd := c.GetKubectlCmd(p, "get", "po", "ohai", "--ignore-not-found")
	return cmd.Run()
}

// TrackKCluster tracks connectivity to a cluster
func TrackKCluster(p *supervisor.Process, rai *RunAsInfo, kargs []string) (*KCluster, error) {
	c := &KCluster{
		rai:   rai,
		kargs: kargs,
	}
	c.doCheck = c.check
	c.doQuit = func() error { c.done = true; return nil }

	if err := c.check(p); err != nil {
		return nil, errors.Wrap(err, "initial cluster check")
	}

	cmd := c.GetKubectlCmd(p, "config", "current-context")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, errors.Wrap(err, "kubectl config current-context")
	}
	c.context = strings.TrimSpace(string(output))

	cmd = c.GetKubectlCmd(p, "config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}")
	output, err = cmd.CombinedOutput()
	if err != nil {
		return nil, errors.Wrap(err, "kubectl config view")
	}
	c.server = strings.TrimSpace(string(output))

	c.setup(p.Supervisor(), "cluster")
	return c, nil
}

// crCmd is a handle to a checked retrying command
type crCmd struct {
	args       []string
	rai        *RunAsInfo
	check      func() error
	startGrace time.Duration
	callerP    *supervisor.Process // processor's Process
	cmd        *supervisor.Cmd     // (run loop) tracks the cmd for killing it
	quitting   bool                // (run loop) enables Close()
	startedAt  time.Time
	ResourceBase
}

// CheckedRetryingCommand launches a command, restarting it repeatedly if it
// quits, and killing and restarting it if it fails the given check.
func CheckedRetryingCommand(
	p *supervisor.Process, name string, args []string, rai *RunAsInfo,
	check func() error, startGrace time.Duration,
) (Resource, error) {
	if check == nil {
		check = func() error { return nil }
	}
	crc := &crCmd{
		args:       args,
		rai:        rai,
		check:      check,
		startGrace: startGrace,
		callerP:    p,
	}
	crc.ResourceBase.doCheck = crc.doCheck
	crc.ResourceBase.doQuit = crc.doQuit
	crc.setup(p.Supervisor(), name)

	if err := crc.launch(); err != nil {
		return nil, errors.Wrapf(err, "initial launch of %s", name)
	}
	return crc, nil
}

func (crc *crCmd) subprocessEnded() error {
	crc.cmd = nil
	if crc.quitting {
		crc.done = true
	}
	return nil
}

func (crc *crCmd) launch() error {
	if crc.cmd != nil {
		panic(fmt.Errorf("launching %s: already launched", crc.name))
	}
	sup := crc.callerP.Supervisor()

	// Launch the subprocess (set up logging using a worker)
	launchErr := make(chan error)
	sup.Supervise(&supervisor.Worker{
		Name: crc.name + "/out",
		Work: func(p *supervisor.Process) error {
			crc.cmd = crc.rai.Command(p, crc.args...)
			launchErr <- crc.cmd.Start()
			return nil
		},
	})

	// Wait for it to start
	select {
	case err := <-launchErr:
		if err != nil {
			return err
		}
	case <-crc.callerP.Shutdown():
		return nil
	}
	crc.startedAt = time.Now()

	// Launch a worker to Wait() for it to finish
	sup.Supervise(&supervisor.Worker{
		Name: crc.name + "/end",
		Work: func(p *supervisor.Process) error {
			// Wait for the subprocess to end. The processor worker will call
			// kill() on shutdown (via quit()) so we don't need to worry about
			// supervisor shutdown ourselves.
			p.Log(crc.cmd.Wait())
			crc.tasks <- crc.subprocessEnded
			return nil
		},
	})

	return nil
}

func (crc *crCmd) kill() error {
	if crc.cmd != nil {
		if err := crc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			crc.callerP.Logf("kill failed (ignoring): %v", err)
		}
	}
	return nil
}

func (crc *crCmd) doQuit() error {
	crc.quitting = true
	return crc.kill()
}

// doCheck determines whether the subprocess is running and healthy
func (crc *crCmd) doCheck(p *supervisor.Process) error {
	if crc.cmd == nil {
		if crc.quitting {
			crc.done = true
			return nil
		}
		crc.tasks <- crc.launch
		return errors.New("not running")
	}
	if err := crc.check(); err != nil {
		p.Logf("check failed: %v", err)
		runTime := time.Since(crc.startedAt)
		if runTime > crc.startGrace {
			// Kill the process because it's in a bad state
			p.Log("Killing...")
			_ = crc.kill()
		} else {
			p.Logf("Not killing yet (%v < %v)", runTime, crc.startGrace)
		}
		return err // from crc.check() above
	}
	return nil
}
