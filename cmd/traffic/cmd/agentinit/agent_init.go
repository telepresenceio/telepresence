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
	"gopkg.in/yaml.v3"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/install/agent"
)

const nat = "nat"
const inboundChain = "TEL_INBOUND"

type config struct {
	agent.Config
}

func loadConfig(ctx context.Context) (*config, error) {
	cf, err := dos.Open(ctx, filepath.Join(agent.ConfigMountPoint, agent.ConfigFile))
	if err != nil {
		return nil, fmt.Errorf("unable to open agent ConfigMap: %w", err)
	}
	defer cf.Close()

	c := config{}
	if err = yaml.NewDecoder(cf).Decode(&c.Config); err != nil {
		return nil, fmt.Errorf("unable to decode agent ConfigMap: %w", err)
	}
	return &c, nil
}

func (c *config) configureIptables(ctx context.Context, iptables *iptables.IPTables, loopback string) error {
	// These iptables rules implement routing such that a packet directed to the appPort will hit the agentPort instead.
	// If there's no mesh this is simply request -> agent -> app (or intercept)
	// However, if there's a service mesh we want to make sure we don't bypass the mesh, so the traffic
	// will flow request -> mesh -> agent -> app

	// A service mesh will typically use an UID different from the one used by this process
	agentUID := strconv.Itoa(os.Getuid())

	outputInsertCount := 0
	for _, proto := range []string{"tcp", "udp"} {
		hasRule := false
		for _, cn := range c.Containers {
			for _, ic := range cn.Intercepts {
				if strings.EqualFold(proto, ic.Protocol) {
					hasRule = true
					break
				}
			}
		}
		if !hasRule {
			// no rules for the given proto
			continue
		}

		// Clearing the inbound chain will create it if it doesn't exist, or clear it out if it does.
		chain := inboundChain + "_" + strings.ToUpper(proto)
		err := iptables.ClearChain(nat, chain)
		if err != nil {
			return fmt.Errorf("failed to clear chain %s: %w", chain, err)
		}

		// Use our inbound chain to direct traffic coming into the app port to the agent port.
		for _, cn := range c.Containers {
			for _, ic := range cn.Intercepts {
				if strings.EqualFold(proto, ic.Protocol) {
					err = iptables.AppendUnique(nat, chain,
						"-p", proto, "--dport", strconv.Itoa(int(ic.ContainerPort)),
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
			"-p", proto,
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
			"-p", proto,
			"!", "-d", "127.0.0.1/32",
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

func findLoopback(ctx context.Context) (string, error) {
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

// Main is the main function for the agent init container
func Main(ctx context.Context, args ...string) error {
	cfg, err := loadConfig(ctx)
	if err != nil {
		return err
	}

	lo, err := findLoopback(ctx)
	if err != nil {
		return err
	}
	it, err := iptables.New()
	if err != nil {
		return fmt.Errorf("unable to create iptables instance: %w", err)
	}
	return cfg.configureIptables(ctx, it, lo)
}
