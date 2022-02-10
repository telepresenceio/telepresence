package cli

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"time"

	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/routing"
)

type vpnDiagInfo struct {
}

func vpnDiagCommand() *cobra.Command {
	di := vpnDiagInfo{}
	cmd := &cobra.Command{
		Use:   "test-vpn",
		Args:  cobra.NoArgs,
		Short: "Test VPN configuration for compatibility with telepresence",
		RunE:  di.run,
	}
	return cmd
}

func waitForNetwork(ctx context.Context) error {
	publicIP := net.ParseIP("8.8.8.8")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		_, err := routing.GetRoute(ctx, &net.IPNet{IP: publicIP, Mask: net.CIDRMask(32, 32)})
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("timed out waiting for a route to %s; this usually means your VPN client is misconfigured", publicIP)
}

const (
	good    = "✅"
	bad     = "❌"
	podType = "pod"
	svcType = "svc"
)

func getLiveInterfaces() ([]net.Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("unable to list network interfaces: %w", err)
	}
	retval := []net.Interface{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp != 0 {
			retval = append(retval, iface)
		}
	}
	return retval, nil
}

func getVPNInterfaces(interfacesConnected, interfacesDisconnected []net.Interface) map[string]struct{} {
	vpnIfaces := map[string]struct{}{}
ifaces:
	for _, ifaceC := range interfacesConnected {
		for _, ifaceD := range interfacesDisconnected {
			if ifaceD.Name == ifaceC.Name {
				continue ifaces
			}
		}
		vpnIfaces[ifaceC.Name] = struct{}{}
	}
	return vpnIfaces
}

func (di *vpnDiagInfo) run(cmd *cobra.Command, _ []string) (err error) {
	var (
		ctx          = cmd.Context()
		sc           = scout.NewReporter(ctx, "cli")
		configIssues = false
		vpnMasks     = false
		clusterMasks = false
		reader       = bufio.NewReader(cmd.InOrStdin())
	)
	sc.Start(log.WithDiscardingLogger(ctx))
	defer sc.Close()

	err = cliutil.QuitDaemon(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			sc.Report(log.WithDiscardingLogger(ctx), "vpn_diag_error", scout.Entry{Key: "error", Value: err.Error()})
		} else {
			if configIssues {
				sc.Report(log.WithDiscardingLogger(ctx), "vpn_diag_fail",
					scout.Entry{Key: "vpn_masks", Value: vpnMasks},
					scout.Entry{Key: "cluster_masks", Value: clusterMasks},
				)
			} else {
				sc.Report(log.WithDiscardingLogger(ctx), "vpn_diag_pass")
			}
		}
	}()

	fmt.Fprintln(cmd.OutOrStdout(), "Please disconnect from your VPN now and hit enter once you're disconnected...")
	_, err = reader.ReadString('\n')
	if err != nil {
		return err
	}
	err = waitForNetwork(ctx)
	if err != nil {
		return err
	}
	interfacesDisconnected, err := getLiveInterfaces()
	if err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Please connect to your VPN now and hit enter once you're connected...")
	_, err = reader.ReadString('\n')
	if err != nil {
		return err
	}

	err = waitForNetwork(ctx)
	if err != nil {
		return err
	}

	interfacesConnected, err := getLiveInterfaces()
	if err != nil {
		return err
	}
	vpnIfaces := getVPNInterfaces(interfacesConnected, interfacesDisconnected)

	routeTable, err := routing.GetRoutingTable(ctx)
	if err != nil {
		return fmt.Errorf("failed to get routing table: %w", err)
	}
	subnets := map[string][]*net.IPNet{podType: {}, svcType: {}}
	err = withConnector(cmd, false, func(ctx context.Context, cc connector.ConnectorClient, _ *connector.ConnectInfo, dc daemon.DaemonClient) error {
		// If this times out, it's likely to be because the traffic manager never gave us the subnets;
		// this could happen for all kinds of reasons, but it makes no sense to go on if it does.
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		clusterSubnets, err := dc.GetClusterSubnets(ctx, &empty.Empty{})
		if err != nil {
			return err
		}
		for _, sn := range clusterSubnets.GetPodSubnets() {
			ipsn := iputil.IPNetFromRPC(sn)
			subnets[podType] = append(subnets[podType], ipsn)
		}
		for _, sn := range clusterSubnets.GetSvcSubnets() {
			ipsn := iputil.IPNetFromRPC(sn)
			subnets[svcType] = append(subnets[svcType], ipsn)
		}
		return nil
	})
	if err != nil {
		return err
	}

	instructions := []string{}
	for _, tp := range []string{podType, svcType} {
		for _, sn := range subnets[tp] {
			ok := true
			for _, rt := range routeTable {
				if _, inVPN := vpnIfaces[rt.Interface.Name]; !inVPN {
					continue
				}
				if rt.Routes(sn.IP) || sn.Contains(rt.RoutedNet.IP) {
					ok = false
					configIssues = true
					snSz, _ := sn.Mask.Size()
					rtSz, _ := rt.RoutedNet.Mask.Size()
					if rtSz > snSz {
						vpnMasks = true
						instructions = append(instructions,
							fmt.Sprintf("%s %s subnet %s being masked by VPN-routed CIDR %s."+
								"This usually means that Telepresence will not be able to connect to your cluster. To resolve:",
								bad, tp, sn, rt.RoutedNet),
							fmt.Sprintf("\t* Move %s subnet %s to a subnet not mapped by the VPN", tp, sn),
							fmt.Sprintf("\t\t* If this is not possible, consider shrinking the mask of the %s CIDR (e.g. from /16 to /8), or disabling split-tunneling", rt.RoutedNet),
						)
					} else {
						clusterMasks = true
						instructions = append(instructions,
							fmt.Sprintf("%s %s subnet %s is masking VPN-routed CIDR %s."+
								"This usually means Telepresence will be able to connect to your cluster, but hosts on your VPN may be inaccessible while telepresence is connected; to resolve:",
								bad, tp, sn, rt.RoutedNet),
							fmt.Sprintf("\t* Move %s subnet %s to a subnet not mapped by the VPN", tp, sn),
							fmt.Sprintf("\t\t* If this is not possible, ensure that any hosts in CIDR %s are placed in the never-proxy list", rt.RoutedNet),
						)
					}
				}
			}
			if ok {
				instructions = append(instructions, fmt.Sprintf("%s %s subnet %s is clear of VPN", good, tp, sn))
			}
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "\n---------- Test Results:")
	for _, instruction := range instructions {
		fmt.Fprintln(cmd.OutOrStdout(), instruction)
	}
	if configIssues {
		fmt.Fprintln(cmd.OutOrStdout(), "\nPlease see https://www.telepresence.io/docs/latest/reference/vpn for more info on these corrective actions, as well as examples")
	}
	fmt.Fprintln(cmd.OutOrStdout(), "\nStill having issues? Please create a new github issue at https://github.com/telepresenceio/telepresence/issues/new?template=Bug_report.md\n",
		"Please make sure to add the following to your issue:\n",
		"* Run `telepresence loglevel debug`, try to connect, then run `telepresence gather_logs`. It will produce a zipfile that you should attach to the issue.\n",
		"* Which VPN client are you using?\n",
		"* Which VPN server are you using?\n",
		"* How is your VPN pushing DNS configuration? It may be useful to add the contents of /etc/resolv.conf")

	return nil
}
