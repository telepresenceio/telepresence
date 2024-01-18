//go:build !windows
// +build !windows

package agentinit

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/coreos/go-iptables/iptables"
	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

const (
	nat          = "nat"
	inboundChain = "TEL_INBOUND"
)

type config struct {
	agentconfig.SidecarExt
}

func loadConfig(ctx context.Context) (*config, error) {
	bs, err := dos.ReadFile(ctx, filepath.Join(agentconfig.ConfigMountPoint, agentconfig.ConfigFile))
	if err != nil {
		return nil, fmt.Errorf("unable to open agent ConfigMap: %w", err)
	}

	c := config{}
	c.SidecarExt, err = agentconfig.UnmarshalYAML(bs)
	if err != nil {
		return nil, fmt.Errorf("unable to decode agent ConfigMap: %w", err)
	}
	return &c, nil
}

func (c *config) configureIptables(_ context.Context, iptables *iptables.IPTables, loopback, localHostCIDR string) error {
	// These iptables rules implement routing such that a packet directed to the appPort will hit the agentPort instead.
	// If there's no mesh this is simply request -> agent -> app (or intercept)
	// However, if there's a service mesh we want to make sure we don't bypass the mesh, so the traffic
	// will flow request -> mesh -> agent -> app

	// A service mesh will typically use an UID different from the one used by this process
	agentUID := strconv.Itoa(os.Getuid())

	outputInsertCount := 0
	for _, proto := range []core.Protocol{core.ProtocolTCP, core.ProtocolUDP} {
		hasRule := false
	nextCn:
		for _, cn := range c.AgentConfig().Containers {
			for _, ic := range agentconfig.PortUniqueIntercepts(cn) {
				if proto == ic.Protocol {
					hasRule = true
					break nextCn
				}
			}
		}
		if !hasRule {
			// no rules for the given proto
			continue
		}

		// Clearing the inbound chain will create it if it doesn't exist, or clear it out if it does.
		chain := inboundChain + "_" + string(proto)
		err := iptables.ClearChain(nat, chain)
		if err != nil {
			return fmt.Errorf("failed to clear chain %s: %w", chain, err)
		}

		// Use our inbound chain to direct traffic coming into the app port to the agent port.
		for _, cn := range c.AgentConfig().Containers {
			for _, ic := range agentconfig.PortUniqueIntercepts(cn) {
				if proto == ic.Protocol {
					err = iptables.AppendUnique(nat, chain,
						"-p", strings.ToLower(string(proto)), "--dport", strconv.Itoa(int(ic.ContainerPort)),
						"-j", "REDIRECT", "--to-ports", strconv.Itoa(int(ic.AgentPort)))
					if err != nil {
						return fmt.Errorf("failed to append rule to %s: %w", chain, err)
					}
					hasRule = true
				}
			}
		}

		// Direct everything coming into PREROUTING into our own inbound chain.
		// We do this as an append instead of an insert because this will prevent us from interfering with a service mesh
		// if one exists. If a service mesh exists, its PREROUTING rules will kick in before ours, ensuring traffic
		// coming into the pod does not bypass the mesh.
		err = iptables.AppendUnique(nat, "PREROUTING",
			"-p", strings.ToLower(string(proto)),
			"-j", chain)
		if err != nil {
			return fmt.Errorf("failed to append prerouting rule to direct to %s: %w", chain, err)
		}

		// Any traffic heading out of the loopback and into the app port (other than traffic from the agent) needs to
		// be redirected to the agent. This will ensure that if there's a service mesh, when the mesh's proxy goes to
		// request the application, it will get a response via the traffic agent.
		err = iptables.Insert(nat, "OUTPUT", 1,
			"-o", loopback,
			"-m", "owner", "!", "--uid-owner", agentUID,
			"-j", chain)
		if err != nil {
			return fmt.Errorf("failed to insert ! --gid-owner rule in OUTPUT: %w", err)
		}
		outputInsertCount++

		// Any agent traffic heading out on the loopback but NOT towards localhost needs to be processed in case
		// it needs to be redirected. This is so that if the traffic agent requests its own IP, it doesn't just
		// serve the app but actually goes through the agent, and thus through any intercepts.
		// This is needed to support requesting an intercepted pod by IP (or to intercept a headless service).
		err = iptables.Insert(nat, "OUTPUT", 1,
			"-o", loopback,
			"-p", strings.ToLower(string(proto)),
			"!", "-d", localHostCIDR,
			"-m", "owner", "--uid-owner", agentUID,
			"-j", chain)
		if err != nil {
			return fmt.Errorf("failed to insert --gid-owner rule in OUTPUT: %w", err)
		}
		outputInsertCount++
	}

	// Finally, any other traffic heading out of the traffic agent should pass by unperturbed -- it should obviously not be
	// redirected back into the agent, but it also should not pass through a mesh proxy.
	// This will include not just agent->manager traffic but also the agent requesting 127.0.0.1:appPort to serve the application
	err := iptables.Insert(nat, "OUTPUT", 1+outputInsertCount,
		"-m", "owner", "--uid-owner", agentUID,
		"-j", "RETURN")
	if err != nil {
		return fmt.Errorf("failed to insert --gid-owner rule in OUTPUT: %w", err)
	}
	return nil
}

func findLoopback() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("failed to get network interfaces: %w", err)
	}
	for _, iface := range ifaces {
		// If the interface is down, we can't use it anyway
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			return iface.Name, nil
		}
	}
	return "", fmt.Errorf("unable to find loopback network interface")
}

// Main is the main function for the agent init container.
func Main(ctx context.Context, args ...string) error {
	dlog.Infof(ctx, "Traffic Agent Init %s", version.Version)
	defer func() {
		if r := recover(); r != nil {
			dlog.Error(ctx, derror.PanicToError(r))
		}
	}()
	cfg, err := loadConfig(ctx)
	if err != nil {
		dlog.Error(ctx, err)
		return err
	}

	lo, err := findLoopback()
	if err != nil {
		dlog.Error(ctx, err)
		return err
	}
	proto := iptables.ProtocolIPv4
	localhostCIDR := "127.0.0.1/32"
	if len(iputil.Parse(os.Getenv("POD_IP"))) == 16 {
		proto = iptables.ProtocolIPv6
		localhostCIDR = "::1/128"
	}
	it, err := iptables.NewWithProtocol(proto)
	if err != nil {
		err = fmt.Errorf("unable to create iptables instance: %w", err)
		dlog.Error(ctx, err)
		return err
	}
	if err = cfg.configureIptables(ctx, it, lo, localhostCIDR); err != nil {
		dlog.Error(ctx, err)
	}
	return err
}
