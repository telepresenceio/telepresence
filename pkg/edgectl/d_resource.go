package edgectl

import (
	"fmt"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/supervisor"
)

// Resource represents one thing managed by edgectl daemon. Examples include
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
	okay    bool          // (monitor) cmd is running and check passes
	transAt time.Time     // (monitor) time of transition (okay value changed)
	done    bool          // (Close) to get everything to quit
	end     chan struct{} // (Close) closed when the processor finishes
}

// Name implements Resource
func (rb *ResourceBase) Name() string {
	res := make(chan string)
	rb.tasks <- func(_ *supervisor.Process) error {
		res <- rb.name
		return nil
	}
	return <-res
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

func (rb *ResourceBase) setup(sup *supervisor.Supervisor, name string) {
	rb.name = name
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
				p.Log("daemon is shutting down")
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
		Notify(p, fmt.Sprintf("%s: %t -> %t after %s", rb.name, old, rb.okay, time.Since(rb.transAt)))
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
			Notify(p, fmt.Sprintf("%s: %t -> Closed after %s", rb.name, rb.okay, time.Since(rb.transAt)))
			p.Log("done")
			return nil
		}
	}
}

// KCluster is a Kubernetes cluster reference
type KCluster struct {
	context      string
	namespace    string
	server       string
	rai          *RunAsInfo
	kargs        []string
	isBridgeOkay func() bool
	ResourceBase
}

// RAI returns the RunAsInfo for this cluster
func (c *KCluster) RAI() *RunAsInfo {
	return c.rai
}

// GetKubectlArgs returns the kubectl command arguments to run a
// kubectl command with this cluster, including the namespace argument.
func (c *KCluster) GetKubectlArgs(args ...string) []string {
	return c.getKubectlArgs(true, args...)
}

// GetKubectlArgsNoNamespace returns the kubectl command arguments to run a
// kubectl command with this cluster, but without the namespace argument.
func (c *KCluster) GetKubectlArgsNoNamespace(args ...string) []string {
	return c.getKubectlArgs(false, args...)
}

func (c *KCluster) getKubectlArgs(includeNamespace bool, args ...string) []string {
	cmdArgs := make([]string, 0, 1+len(c.kargs)+len(args))
	cmdArgs = append(cmdArgs, "kubectl")
	if c.context != "" {
		cmdArgs = append(cmdArgs, "--context", c.context)
	}

	if includeNamespace {
		if c.namespace != "" {
			cmdArgs = append(cmdArgs, "--namespace", c.namespace)
		}
	}

	cmdArgs = append(cmdArgs, c.kargs...)
	cmdArgs = append(cmdArgs, args...)
	return cmdArgs
}

// GetKubectlCmd returns a Cmd that runs kubectl with the given arguments and
// the appropriate environment to talk to the cluster
func (c *KCluster) GetKubectlCmd(p *supervisor.Process, args ...string) *supervisor.Cmd {
	return c.rai.Command(p, c.GetKubectlArgs(args...)...)
}

// GetKubectlCmdNoNamespace returns a Cmd that runs kubectl with the given arguments and
// the appropriate environment to talk to the cluster, but it doesn't supply a namespace
// arg.
func (c *KCluster) GetKubectlCmdNoNamespace(p *supervisor.Process, args ...string) *supervisor.Cmd {
	return c.rai.Command(p, c.GetKubectlArgsNoNamespace(args...)...)
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
func TrackKCluster(
	p *supervisor.Process, rai *RunAsInfo, context, namespace string, kargs []string,
) (*KCluster, error) {
	c := &KCluster{
		rai:       rai,
		kargs:     kargs,
		context:   context,
		namespace: namespace,
	}
	c.doCheck = c.check
	c.doQuit = func(p *supervisor.Process) error { c.done = true; return nil }

	if err := c.check(p); err != nil {
		return nil, errors.Wrap(err, "initial cluster check")
	}

	if c.context == "" {
		cmd := c.GetKubectlCmd(p, "config", "current-context")
		p.Logf("%s %v", cmd.Path, cmd.Args[1:])
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, errors.Wrap(err, "kubectl config current-context")
		}
		c.context = strings.TrimSpace(string(output))
	}
	p.Logf("Context: %s", c.context)

	if c.namespace == "" {
		nsQuery := fmt.Sprintf("jsonpath={.contexts[?(@.name==\"%s\")].context.namespace}", c.context)
		cmd := c.GetKubectlCmd(p, "config", "view", "-o", nsQuery)
		p.Logf("%s %v", cmd.Path, cmd.Args[1:])
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, errors.Wrap(err, "kubectl config view ns")
		}
		c.namespace = strings.TrimSpace(string(output))
		if c.namespace == "" { // This is what kubens does
			c.namespace = "default"
		}
	}
	p.Logf("Namespace: %s", c.namespace)

	cmd := c.GetKubectlCmd(p, "config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}")
	p.Logf("%s %v", cmd.Path, cmd.Args[1:])
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, errors.Wrap(err, "kubectl config view server")
	}
	c.server = strings.TrimSpace(string(output))
	p.Logf("Server: %s", c.server)

	c.setup(p.Supervisor(), "cluster")
	return c, nil
}

// crCmd is a handle to a checked retrying command
type crCmd struct {
	args       []string
	rai        *RunAsInfo
	check      func(p *supervisor.Process) error
	startGrace time.Duration
	cmd        *supervisor.Cmd // (run loop) tracks the cmd for killing it
	quitting   bool            // (run loop) enables Close()
	startedAt  time.Time
	ResourceBase
}

// CheckedRetryingCommand launches a command, restarting it repeatedly if it
// quits, and killing and restarting it if it fails the given check.
func CheckedRetryingCommand(
	p *supervisor.Process, name string, args []string, rai *RunAsInfo,
	check func(*supervisor.Process) error, startGrace time.Duration,
) (Resource, error) {
	if check == nil {
		check = func(*supervisor.Process) error { return nil }
	}
	crc := &crCmd{
		args:       args,
		rai:        rai,
		check:      check,
		startGrace: startGrace,
	}
	crc.ResourceBase.doCheck = crc.doCheck
	crc.ResourceBase.doQuit = crc.doQuit
	crc.setup(p.Supervisor(), name)

	if err := crc.launch(p); err != nil {
		return nil, errors.Wrapf(err, "initial launch of %s", name)
	}
	return crc, nil
}

func (crc *crCmd) subprocessEnded(p *supervisor.Process) error {
	p.Log("end: subprocess ended")
	crc.cmd = nil
	if crc.quitting {
		p.Log("end: marking as done")
		crc.done = true
	}
	return nil
}

func (crc *crCmd) launch(p *supervisor.Process) error {
	if crc.cmd != nil {
		panic(fmt.Errorf("launching %s: already launched", crc.name))
	}

	// Launch the subprocess (set up logging using a worker)
	p.Logf("Launching %s...", crc.name)
	launchErr := make(chan error)
	p.Supervisor().Supervise(&supervisor.Worker{
		Name: crc.name + "/out",
		Work: func(p *supervisor.Process) error {
			crc.cmd = crc.rai.Command(p, crc.args...)
			launchErr <- crc.cmd.Start()
			// Wait for the subprocess to end. Another worker will
			// call kill() on shutdown (via quit()) so we don't need
			// to worry about supervisor shutdown ourselves.
			if err := crc.cmd.Wait(); err != nil {
				p.Log(err)
			}
			crc.tasks <- crc.subprocessEnded
			return nil
		},
	})

	// Wait for it to start
	select {
	case err := <-launchErr:
		if err != nil {
			return err
		}
	case <-p.Shutdown():
		return nil
	}
	crc.startedAt = time.Now()
	p.Logf("Launched %s", crc.name)

	return nil
}

func (crc *crCmd) kill(p *supervisor.Process) error {
	if crc.cmd != nil {
		p.Log("kill: sending signal")
		if err := crc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			p.Logf("kill: failed (ignoring): %v", err)
		}
	} else {
		p.Log("kill: no subprocess to kill")
	}
	return nil
}

func (crc *crCmd) doQuit(p *supervisor.Process) error {
	crc.quitting = true
	return crc.kill(p)
}

// doCheck determines whether the subprocess is running and healthy
func (crc *crCmd) doCheck(p *supervisor.Process) error {
	if crc.cmd == nil {
		if crc.quitting {
			p.Log("check: no subprocess + quitting -> done")
			crc.done = true
			return nil
		}
		p.Log("check: no subprocess -> launch")
		crc.tasks <- crc.launch
		return errors.New("not running")
	}
	if err := crc.check(p); err != nil {
		p.Logf("check: failed: %v", err)
		runTime := time.Since(crc.startedAt)
		if runTime > crc.startGrace {
			// Kill the process because it's in a bad state
			p.Log("check: killing...")
			_ = crc.kill(p)
		} else {
			p.Logf("check: not killing yet (%v < %v)", runTime, crc.startGrace)
		}
		return err // from crc.check() above
	}
	p.Log("check: passed")
	return nil
}
