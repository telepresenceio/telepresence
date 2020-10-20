package teleproxy

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/pkg/errors"

	"github.com/datawire/telepresence2/pkg/bridges"
	"github.com/datawire/telepresence2/pkg/interceptor"
)

const (
	interceptMode = "intercept"
	bridgeMode    = "bridge"
	versionMode   = "version"
)

// worker names
const (
	Worker       = "TPY"
	SignalWorker = "SIG"
)

var logLegend = []struct {
	Prefix      string
	Description string
}{
	{Worker, "The setup worker launches all the other workers."},
	{bridges.BridgeWorker, "The bridge worker sets up the kubernetes and docker bridges."},
	{bridges.K8sBridgeWorker, "The kubernetes bridge."},
	{bridges.K8sPortForwardWorker, "The kubernetes port forward used for connectivity."},
	{bridges.K8sSSHWorker, "The SSH port forward used on top of the kubernetes port forward."},
	{bridges.K8sApplyWorker, "The kubernetes apply used to setup the in-cluster pod we talk with."},
	{bridges.DkrBridgeWorker, "The docker bridge."},
	{interceptor.CheckReadyWorker, "The worker teleproxy uses to do a self check and signal the system it is ready."},
	{interceptor.DNSServerWorker, "The DNS server teleproxy runs to intercept dns requests."},
	{interceptor.TranslatorWorker, "The network address translator controls the system firewall settings used to " +
		"intercept ip addresses."},
	{interceptor.ProxyWorker, "The proxy forwards connections to intercepted addresses to the configured destinations."},
	{interceptor.APIWorker, "The API handles requests that allow viewing and updating the routing table that maintains " +
		"the set of dns names and ip addresses that should be intercepted."},
}

// config holds the configuration for a Teleproxy
type config struct {
	Mode       string
	KubeConfig string
	Context    string
	Namespace  string
	DNSIP      string
	FallbackIP string
	NoSearch   bool
	NoCheck    bool
	Version    bool
}

// run is the main entry point for Teleproxy
func (t *config) run(version string) error {
	if t.Version {
		t.Mode = versionMode
	}

	switch t.Mode {
	case interceptMode, bridgeMode:
		// do nothing
	case versionMode:
		fmt.Println("teleproxy", "version", version)
		return nil
	default:
		return errors.Errorf("TPY: unrecognized mode: %v", t.Mode)
	}

	// do this up front so we don't miss out on cleanup if someone
	// Control-C's just after starting us
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	ctx, cancel := context.WithCancel(context.Background())
	sup := supervisor.WithContext(ctx)

	var bs bridges.Service
	sup.Supervise(&supervisor.Worker{
		Name: SignalWorker,
		Work: func(p *supervisor.Process) error {
			for {
				select {
				case <-p.Shutdown():
					return nil
				case s := <-signalChan:
					p.Logf("TPY: %v", s)
					if s == syscall.SIGHUP && bs != nil {
						bs.Restart()
					} else {
						cancel()
						return nil
					}
				}
			}
		},
	})

	sup.Supervise(&supervisor.Worker{
		Name: Worker,
		Work: func(p *supervisor.Process) error {
			switch t.Mode {
			case interceptMode:
				return interceptor.Start(p, t.DNSIP, t.FallbackIP, t.NoCheck, t.NoSearch)
			case bridgeMode:
				bs = bridges.NewService(t.KubeConfig, t.Context, t.Namespace)
				return bs.Start(p)
			default:
				return nil
			}
		},
	})

	log.Println("Log prefixes used by the different teleproxy workers:")
	log.Println("")
	for _, entry := range logLegend {
		log.Printf("  %s -> %s\n", entry.Prefix, entry.Description)
	}
	log.Println("")

	errs := sup.Run()
	if len(errs) == 0 {
		fmt.Println("Teleproxy exited successfully")
		return nil
	}

	msg := fmt.Sprintf("Teleproxy exited with %d error(s):\n", len(errs))

	for _, err := range errs {
		msg += fmt.Sprintf("  %v\n", err)
	}

	return errors.New(strings.TrimSpace(msg))
}
