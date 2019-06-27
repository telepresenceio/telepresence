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
	name     string
	doCheck  func(*supervisor.Process) error
	doQuit   func() error
	tasks    chan func() error
	quitting bool // (Close) to get everything to quit
	okay     bool // (monitor) cmd is running and check passes
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
	done := make(chan struct{})
	rb.tasks <- func() error {
		defer close(done)
		return rb.quit()
	}
	<-done

	// FIXME: Wait until things have closed?
	return nil
}

func (rb *ResourceBase) quit() error {
	rb.quitting = true
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
	p.Ready()
	for {
		var task func() error
		select {
		case fn := <-rb.tasks: // There is work to do
			task = fn
		case <-ticker.C: // Ticker says it's time to monitor
			task = func() error { return rb.monitor(p) }
		case <-p.Shutdown(): // Supervisor told us to quit
			task = rb.quit
		}
		if err := task(); err != nil {
			return err
		}
		if rb.quitting {
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
func TrackKCluster(p *supervisor.Process, args *ConnectArgs) (*KCluster, error) {
	c := &KCluster{
		rai:   args.RAI,
		kargs: args.KArgs,
		ResourceBase: ResourceBase{
			name:  "cluster",
			tasks: make(chan func() error, 1),
		},
	}
	c.doCheck = c.check
	c.doQuit = func() error { return nil }

	if err := c.check(p); err != nil {
		return nil, err
	}

	cmd := c.GetKubectlCmd(p, "config", "current-context")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	c.context = strings.TrimSpace(string(output))

	cmd = c.GetKubectlCmd(p, "config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}")
	output, err = cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	c.server = strings.TrimSpace(string(output))

	p.Supervisor().Supervise(&supervisor.Worker{
		Name: "cluster",
		Work: c.processor,
	})

	return c, nil
}

// crCmd is a handle to a checked retrying command
type crCmd struct {
	args    []string
	rai     *RunAsInfo
	check   func() error
	callerP *supervisor.Process // processor's Process
	cmd     *supervisor.Cmd     // (run loop) tracks the cmd for killing it
	ResourceBase
}

// CheckedRetryingCommand launches a command, restarting it repeatedly if it
// quits, and killing and restarting it if it fails the given check.
func CheckedRetryingCommand(
	p *supervisor.Process, name string, args []string, rai *RunAsInfo, check func() error,
) (Resource, error) {
	if check == nil {
		check = func() error { return nil }
	}
	crc := &crCmd{
		args:    args,
		rai:     rai,
		check:   check,
		callerP: p,
		ResourceBase: ResourceBase{
			name:  name,
			tasks: make(chan func() error, 1),
		},
	}
	crc.ResourceBase.doCheck = crc.doCheck
	crc.ResourceBase.doQuit = crc.doQuit

	p.Supervisor().Supervise(&supervisor.Worker{
		Name: crc.name + "/crc",
		Work: crc.processor,
	})
	if err := crc.launch(); err != nil {
		return nil, err
	}
	return crc, nil
}

func (crc *crCmd) launch() error {
	if crc.cmd != nil {
		panic(fmt.Errorf("launching %s: already launched", crc.name))
	}
	launchErr := make(chan error)
	crc.callerP.Supervisor().Supervise(&supervisor.Worker{
		Name: crc.name + "/proc",
		Work: func(p *supervisor.Process) error {
			// Launch the subprocess
			crc.cmd = crc.rai.Command(p, crc.args...)
			if err := crc.cmd.Start(); err != nil {
				launchErr <- err
				return nil
			}
			launchErr <- nil
			p.Ready()

			// Wait for the subprocess to end, log
			p.Logf("subprocess ended: %v", p.DoClean(crc.cmd.Wait, crc.kill))
			crc.cmd = nil

			return nil
		},
	})
	select {
	case err := <-launchErr:
		return err
	case <-crc.callerP.Shutdown():
		return nil
	}
}

func (crc *crCmd) kill() error {
	if crc.cmd != nil {
		return crc.cmd.Process.Signal(syscall.SIGTERM)
	}
	return nil // Or fmt.Errorf("trying to kill non-running subprocess for %s", crc.name)
}

func (crc *crCmd) doQuit() error {
	crc.quitting = true
	return crc.kill()
}

// doCheck determines whether the subprocess is running and healthy
func (crc *crCmd) doCheck(p *supervisor.Process) error {
	if crc.cmd == nil {
		crc.tasks <- crc.launch
		return errors.New("not running")
	}
	if err := crc.check(); err != nil {
		p.Logf("check failed: %v", err)
		// Kill the process because it's in a bad state
		if err := crc.kill(); err != nil {
			p.Logf("failed to kill: %v", err)
		}
		return err // from crc.check() above
	}
	return nil
}
