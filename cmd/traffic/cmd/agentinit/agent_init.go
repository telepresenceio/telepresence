//go:build !windows

package agentinit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/coreos/go-iptables/iptables"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

const (
	nat = "nat"
)

type config struct {
	*agentconfig.Sidecar
}

type iptablesConfigurer interface {
	ClearChain(table, chain string) error
	AppendUnique(table, chain string, rulespec ...string) error
	Insert(table, chain string, pos int, rulespec ...string) error
}

func loadConfig() (*config, error) {
	cfgTight, ok := os.LookupEnv(agentconfig.EnvAgentConfig)
	if !ok {
		return nil, errors.New("unable to retrieve agent ConfigMap entry")
	}

	c := config{}
	var err error
	c.Sidecar, err = agentconfig.UnmarshalJSON(cfgTight)
	if err != nil {
		return nil, fmt.Errorf("unable to decode agent ConfigMap: %w", err)
	}
	return &c, nil
}

func trafficAgentUID() (string, error) {
	if uid, ok := os.LookupEnv(agentconfig.EnvAgentUID); ok && uid != "" {
		parsed, err := strconv.ParseUint(uid, 10, 32)
		if err != nil {
			return "", fmt.Errorf("invalid %s %q: %w", agentconfig.EnvAgentUID, uid, err)
		}
		return strconv.FormatUint(parsed, 10), nil
	}
	return strconv.Itoa(os.Getuid()), nil
}

func (c *config) configureIptables(ctx context.Context, ipt iptablesConfigurer, loopback string, localHostCIDR netip.Prefix, podIP netip.Addr) error {
	// These iptables rules implement routing such that a packet directed to the appPort will hit the agentPort instead.
	// If there's no mesh this is simply request -> agent -> app (or intercept)
	// However, if there's a service mesh we want to make sure we don't bypass the mesh, so the traffic
	// will flow request -> mesh -> agent -> app

	// A service mesh will typically use a UID different from the traffic-agent.
	agentUID, err := trafficAgentUID()
	if err != nil {
		return err
	}

	outputInsertCount := 0
	for _, proto := range []types.Proto{types.ProtoTCP, types.ProtoUDP} {
		hasRule := false
	nextCn:
		for _, cn := range c.Containers {
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

		// Clearing the chains will create them if they don't exist or clear them out if they do.
		protoStr := proto.String()
		preRoutingChain := "TEL_PREROUTING_" + protoStr
		err := ipt.ClearChain(nat, preRoutingChain)
		if err != nil {
			return fmt.Errorf("failed to clear chain %s: %w", preRoutingChain, err)
		}
		outputChain := "TEL_OUTPUT_" + protoStr
		err = ipt.ClearChain(nat, outputChain)
		if err != nil {
			return fmt.Errorf("failed to clear chain %s: %w", outputChain, err)
		}

		// Use our inbound chain to direct traffic coming into the app port to the agent port.
		lcProto := strings.ToLower(protoStr)
		for _, cn := range c.Containers {
			for _, ic := range agentconfig.PortUniqueIntercepts(cn) {
				if proto == ic.Protocol {
					// Add REDIRECT to both PREROUTING and OUTPUT, because we want connections that
					// originate from the agent to be subjected to this rule.
					clog.Debugf(ctx, "preroute redirect %d -> %d", ic.ContainerPort, ic.AgentPort)
					err = ipt.AppendUnique(nat, preRoutingChain,
						"-p", lcProto, "--dport", strconv.Itoa(int(ic.ContainerPort)),
						"-j", "REDIRECT", "--to-ports", strconv.Itoa(int(ic.AgentPort)))
					if err != nil {
						return fmt.Errorf("failed to append rule to %s: %w", preRoutingChain, err)
					}
					clog.Debugf(ctx, "output redirect %d -> %d", ic.ContainerPort, ic.AgentPort)
					err = ipt.AppendUnique(nat, outputChain,
						"-p", lcProto, "--dport", strconv.Itoa(int(ic.ContainerPort)),
						"-j", "REDIRECT", "--to-ports", strconv.Itoa(int(ic.AgentPort)))
					if err != nil {
						return fmt.Errorf("failed to append rule to %s: %w", outputChain, err)
					}
					if ic.TargetPortNumeric {
						// The agent forwarder will not write directly to the container port when it is inactive.
						// Instead, it writes to a proxy port and relies on it being redirected to the
						// container port here.
						// Why? Because if it wrote directly to the container port, we wouldn't be able to
						// prevent an endless loop that would otherwise occur here when the previous rule would
						// loop it back into the agent.
						clog.Debugf(ctx, "output DNAT %s:%d -> %s:%d", podIP, c.ProxyPort(ic.AgentPort), podIP, ic.ContainerPort)
						err = ipt.AppendUnique(nat, outputChain,
							"-p", lcProto, "-d", podIP.String(), "--dport", strconv.Itoa(int(c.ProxyPort(ic.AgentPort))),
							"-j", "DNAT", "--to-destination", netip.AddrPortFrom(podIP, ic.ContainerPort).String())
						if err != nil {
							return fmt.Errorf("failed to append rule to %s: %w", outputChain, err)
						}
					}
				}
			}
		}

		// Direct everything coming into PREROUTING into our own inbound chain.
		// We do this as an append instead of an insert because this will prevent us from interfering with a service mesh
		// if one exists. If a service mesh exists, its PREROUTING rules will kick in before ours, ensuring traffic
		// coming into the pod does not bypass the mesh.
		err = ipt.AppendUnique(nat, "PREROUTING",
			"-p", lcProto,
			"-j", preRoutingChain)
		if err != nil {
			return fmt.Errorf("failed to append prerouting rule to direct to %s: %w", preRoutingChain, err)
		}

		// Any traffic heading out of the loopback and into the app port (other than traffic from the agent) needs to
		// be redirected to the agent. This will ensure that if there's a service mesh, when the mesh's proxy goes to
		// request the application, it will get a response via the traffic agent.
		err = ipt.Insert(nat, "OUTPUT", 1,
			"-o", loopback,
			"-p", lcProto,
			"-m", "owner", "!", "--uid-owner", agentUID,
			"-j", outputChain)
		if err != nil {
			return fmt.Errorf("failed to insert ! --uid-owner rule in OUTPUT: %w", err)
		}
		outputInsertCount++

		// Some service meshes connect back to the app through the pod IP rather than a loopback
		// address. Route those connections through the same output chain.
		err = ipt.Insert(nat, "OUTPUT", 1,
			"-p", lcProto,
			"-d", podIP.String(),
			"-m", "owner", "!", "--uid-owner", agentUID,
			"-j", outputChain)
		if err != nil {
			return fmt.Errorf("failed to insert pod IP ! --uid-owner rule in OUTPUT: %w", err)
		}
		outputInsertCount++

		// Any agent traffic heading out on the loopback but NOT towards localhost needs to be processed in case
		// it needs to be redirected. This is so that if the traffic agent requests its own IP, it doesn't just
		// serve the app but actually goes through the agent, and thus through any intercepts.
		// This is needed to support requesting an intercepted pod by IP (or to intercept a headless service).
		err = ipt.Insert(nat, "OUTPUT", 1,
			"-o", loopback,
			"-p", lcProto,
			"!", "-d", localHostCIDR.String(),
			"-m", "owner", "--uid-owner", agentUID,
			"-j", outputChain)
		if err != nil {
			return fmt.Errorf("failed to insert --uid-owner rule in OUTPUT: %w", err)
		}
		outputInsertCount++

		// The traffic agent also uses the pod IP proxy port when forwarding to the
		// inactive app container. Ensure that path works even when the pod IP does
		// not route over the loopback interface.
		err = ipt.Insert(nat, "OUTPUT", 1,
			"-p", lcProto,
			"-d", podIP.String(),
			"-m", "owner", "--uid-owner", agentUID,
			"-j", outputChain)
		if err != nil {
			return fmt.Errorf("failed to insert pod IP --uid-owner rule in OUTPUT: %w", err)
		}
		outputInsertCount++
	}

	// Finally, any other traffic heading out of the traffic agent should pass by unperturbed -- it should obviously not be
	// redirected back into the agent, but it also should not pass through a mesh proxy.
	// This will include not just agent->manager traffic but also the agent requesting 127.0.0.1:appPort to serve the application
	err = ipt.Insert(nat, "OUTPUT", 1+outputInsertCount,
		"-m", "owner", "--uid-owner", agentUID,
		"-j", "RETURN")
	if err != nil {
		return fmt.Errorf("failed to insert --uid-owner rule in OUTPUT: %w", err)
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
	debug.SetTraceback("single")
	clog.Infof(ctx, "Traffic Agent Init %s", version.Version)
	cfg, err := loadConfig()
	if err != nil {
		clog.Error(ctx, err)
		return err
	}

	lo, err := findLoopback()
	if err != nil {
		clog.Error(ctx, err)
		return err
	}
	proto := iptables.ProtocolIPv4
	localhostCIDR := netip.PrefixFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 32)
	podIP, err := netip.ParseAddr(os.Getenv("POD_IP"))
	if err != nil {
		clog.Error(ctx, err)
		return err
	}
	if podIP.Is6() {
		proto = iptables.ProtocolIPv6
		localhostCIDR = netip.PrefixFrom(netip.IPv6Loopback(), 128)
	}
	it, err := iptables.NewWithProtocol(proto)
	if err != nil {
		err = fmt.Errorf("unable to create iptables instance: %w", err)
		clog.Error(ctx, err)
		return err
	}
	if err = cfg.configureIptables(ctx, it, lo, localhostCIDR, podIP); err != nil {
		clog.Error(ctx, err)
	}
	return err
}
