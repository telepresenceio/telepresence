package edgectl

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/supervisor"
)

var DaemonHelp = `The Edge Control Daemon is a long-lived background component that manages
connections and network state.

Launch the Edge Control Daemon:
    sudo edgectl daemon

Examine the Daemon's log output in
    ` + logfile + `
to troubleshoot problems.
`

// Daemon represents the state of the Edge Control Daemon
type Daemon struct {
	network    Resource
	cluster    *KCluster
	bridge     Resource
	trafficMgr *TrafficManager
	intercepts []*Intercept
	dns        string
	fallback   string
}

// RunAsDaemon is the main function when executing as the daemon
func RunAsDaemon(dns, fallback string) error {
	if os.Geteuid() != 0 {
		return errors.New("edgectl daemon must run as root")
	}

	d := &Daemon{dns: dns, fallback: fallback}

	sup := supervisor.WithContext(context.Background())
	sup.Logger = SetUpLogging()
	sup.Supervise(&supervisor.Worker{
		Name: "daemon",
		Work: d.acceptLoop,
	})
	sup.Supervise(&supervisor.Worker{
		Name:     "signal",
		Requires: []string{"daemon"},
		Work:     WaitForSignal,
	})
	sup.Supervise(&supervisor.Worker{
		Name:     "setup",
		Requires: []string{"daemon"},
		Work: func(p *supervisor.Process) error {
			if err := d.MakeNetOverride(p); err != nil {
				return err
			}
			p.Ready()
			return nil
		},
	})

	sup.Logger.Printf("---")
	sup.Logger.Printf("Edge Control daemon %s starting...", DisplayVersion)
	sup.Logger.Printf("PID is %d", os.Getpid())
	runErrors := sup.Run()

	sup.Logger.Printf("")
	if len(runErrors) > 0 {
		sup.Logger.Printf("Daemon has exited with %d error(s):", len(runErrors))
		for _, err := range runErrors {
			sup.Logger.Printf("- %v", err)
		}
	}
	sup.Logger.Printf("Edge Control daemon %s is done.", DisplayVersion)
	return errors.New("edgectl daemon has exited")
}

func (d *Daemon) acceptLoop(p *supervisor.Process) error {
	// Listen on unix domain socket
	unixListener, err := net.Listen("unix", socketName)
	if err != nil {
		return errors.Wrap(err, "chmod")
	}
	err = os.Chmod(socketName, 0777)
	if err != nil {
		return errors.Wrap(err, "chmod")
	}

	p.Ready()
	Notify(p, "Running")
	defer Notify(p, "Shutting down...")

	return p.DoClean(
		func() error {
			for {
				conn, err := unixListener.Accept()
				if err != nil {
					return errors.Wrap(err, "accept")
				}
				_ = p.Go(func(p *supervisor.Process) error {
					return d.handle(p, conn)
				})
			}
		},
		unixListener.Close,
	)
}

func (d *Daemon) handle(p *supervisor.Process, conn net.Conn) error {
	defer conn.Close()

	decoder := json.NewDecoder(conn)
	data := &ClientMessage{}
	if err := decoder.Decode(data); err != nil {
		p.Logf("Failed to read message: %v", err)
		fmt.Fprintln(conn, "API mismatch. Server", DisplayVersion)
		return nil
	}
	if data.APIVersion != apiVersion {
		p.Logf("API version mismatch (got %d, need %d)", data.APIVersion, apiVersion)
		fmt.Fprintf(conn, "API version mismatch (got %d, server %s)", data.APIVersion, DisplayVersion)
		return nil
	}
	p.Logf("Received command: %q", data.Args)

	err := d.handleCommand(p, conn, data)
	if err != nil {
		p.Logf("Command processing failed: %v", err)
	}

	p.Log("Done")
	return nil
}
