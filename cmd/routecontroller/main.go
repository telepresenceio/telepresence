//go:build linux

package main

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/google/nftables"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/telepresenceio/clog"
	tplog "github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/nftutil"
	"github.com/telepresenceio/telepresence/v2/pkg/routenft"
	"github.com/telepresenceio/telepresence/v2/pkg/sigctx"
)

// allFamilies lists every address family routenft manages, in the fixed
// order both installSubnetBlackholes and removeSubnetBlackholes iterate.
//
//nolint:gochecknoglobals // constant
var allFamilies = []nftables.TableFamily{nftables.TableFamilyIPv4, nftables.TableFamilyIPv6}

func main() {
	ctx := context.Background()
	logLevel := os.Getenv("LOG_LEVEL")
	ctx = tplog.MakeBaseLogger(ctx, os.Stdout, logLevel)

	if err := sigctx.DoWithSignalHandler(ctx, run); err != nil {
		clog.Error(ctx, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to create clientset: %w", err)
	}

	installSubnetBlackholes(ctx, cs)
	defer removeSubnetBlackholes(ctx)

	clog.Info(ctx, "Route controller started")
	<-ctx.Done()
	return nil
}

// discoverServiceCIDRs returns the service CIDRs for which nftables FORWARD
// DROP rules will be installed. It first checks the SERVICE_CIDRS environment
// variable (comma-separated list of CIDRs). If that is not set, it queries
// the Kubernetes ServiceCIDR API (available in k8s 1.33+). If neither is
// available it returns nil and no subnet-level protection is installed; set
// SERVICE_CIDRS explicitly in that case.
func discoverServiceCIDRs(ctx context.Context, cs *kubernetes.Clientset) []string {
	if envCIDRs := os.Getenv("SERVICE_CIDRS"); envCIDRs != "" {
		cidrs := strings.Split(envCIDRs, ",")
		clog.Infof(ctx, "Using service CIDRs from SERVICE_CIDRS env: %v", cidrs)
		return cidrs
	}

	list, err := cs.NetworkingV1().ServiceCIDRs().List(ctx, meta.ListOptions{})
	if err != nil {
		clog.Warnf(ctx, "ServiceCIDR API unavailable: %v; set SERVICE_CIDRS to enable subnet blackholing", err)
		return nil
	}
	var cidrs []string
	for _, item := range list.Items {
		cidrs = append(cidrs, item.Spec.CIDRs...)
	}
	clog.Infof(ctx, "Discovered service CIDRs from cluster API: %v", cidrs)
	return cidrs
}

// discoverPodCIDRs returns the pod CIDRs assigned to the node named by the
// NODE_NAME environment variable. It returns nil, and the pod CIDR
// protection is not installed, when the variable is unset or the node has no
// pod CIDR assigned.
func discoverPodCIDRs(ctx context.Context, cs *kubernetes.Clientset) []string {
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		clog.Warn(ctx, "NODE_NAME is not set; pod CIDR blackholing not installed")
		return nil
	}
	node, err := cs.CoreV1().Nodes().Get(ctx, nodeName, meta.GetOptions{})
	if err != nil {
		clog.Warnf(ctx, "Failed to get node %s: %v; pod CIDR blackholing not installed", nodeName, err)
		return nil
	}
	cidrs := node.Spec.PodCIDRs
	if len(cidrs) == 0 && node.Spec.PodCIDR != "" {
		cidrs = []string{node.Spec.PodCIDR}
	}
	clog.Infof(ctx, "Discovered pod CIDRs of node %s: %v", nodeName, cidrs)
	return cidrs
}

// splitFamilies parses cidrs and returns them masked, split by address
// family. Entries that fail to parse are logged and skipped.
func splitFamilies(ctx context.Context, kind string, cidrs []string) (v4, v6 []netip.Prefix) {
	for _, cidr := range cidrs {
		cidr = strings.TrimSpace(cidr)
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			clog.Errorf(ctx, "Failed to parse %s CIDR %q: %v", kind, cidr, err)
			continue
		}
		prefix = prefix.Masked()
		if prefix.Addr().Is4() {
			v4 = append(v4, prefix)
		} else {
			v6 = append(v6, prefix)
		}
	}
	return v4, v6
}

// installSubnetBlackholes builds and applies a native nftables ruleset (see
// pkg/routenft) that drops FORWARD-chain traffic destined for the discovered
// service CIDRs, and traffic destined for the node's own pod CIDRs that
// leaves through the node's default route -- one ruleset per address family
// that has at least one CIDR.
//
// A dedicated nftables `telepresence` filter table with a forward-hook chain
// (rather than a kernel blackhole route) is used because RTN_BLACKHOLE routes
// fail connect()/sendmsg() at the socket level before any netfilter hook can
// fire, which breaks locally-generated traffic such as kube-apiserver ->
// mutating-webhook calls.
//
// The forward hook only affects traffic forwarded through the host (i.e. pod
// traffic via veth pairs). Locally-generated host traffic never reaches the
// forward hook.
//
// For active services, kube-proxy's prerouting DNAT fires before the forward
// hook and rewrites the destination from ClusterIP to a pod IP, so the drop
// rule (which matches the set of original service CIDRs) does not apply. For
// deleted or never-assigned ClusterIPs no DNAT rule exists, the forward hook
// sees the original ClusterIP, and the drop fires.
//
// A pod that exists is reached through the route of its own veth, so traffic
// to the node's pod CIDR that is about to leave through the default route is
// addressed to a pod that is gone. Without the drop it escapes the node the
// same way a deleted ClusterIP does.
func installSubnetBlackholes(ctx context.Context, cs *kubernetes.Clientset) {
	svc4, svc6 := splitFamilies(ctx, "service", discoverServiceCIDRs(ctx, cs))
	pod4, pod6 := splitFamilies(ctx, "pod", discoverPodCIDRs(ctx, cs))
	applyFamily(ctx, nftables.TableFamilyIPv4, svc4, pod4)
	applyFamily(ctx, nftables.TableFamilyIPv6, svc6, pod6)
}

// podUplinks returns the interfaces through which the pod CIDR drop applies:
// those carrying family's default routes. It returns nil, and logs why, when
// there are none.
func podUplinks(ctx context.Context, family nftables.TableFamily) []string {
	links, err := uplinks(ctx, family)
	switch {
	case err != nil:
		clog.Errorf(ctx, "%v; pod CIDR blackholing not installed for family %d", err, family)
	case len(links) == 0:
		clog.Warnf(ctx, "No default route for family %d; pod CIDR blackholing not installed", family)
	}
	return links
}

// applyFamily builds and applies the routenft ruleset for family's CIDRs. It
// is a no-op when there is nothing to install for the family.
func applyFamily(ctx context.Context, family nftables.TableFamily, serviceCIDRs, podCIDRs []netip.Prefix) {
	cfg := routenft.Config{Family: family, ServiceCIDRs: serviceCIDRs}
	if len(podCIDRs) > 0 {
		if cfg.Uplinks = podUplinks(ctx, family); len(cfg.Uplinks) > 0 {
			cfg.PodCIDRs = podCIDRs
		}
	}
	if len(cfg.ServiceCIDRs) == 0 && len(cfg.PodCIDRs) == 0 {
		clog.Debugf(ctx, "No CIDRs for family %d; nftables blackhole not installed", family)
		return
	}
	rs, err := routenft.Build(cfg)
	if err != nil {
		clog.Errorf(ctx, "Failed to build nftables ruleset for family %d: %v", family, err)
		return
	}
	if err := nftutil.Apply(ctx, &rs.Ruleset); err != nil {
		clog.Errorf(ctx, "Failed to apply nftables FORWARD DROP for family %d: %v", family, err)
		return
	}
	if len(cfg.ServiceCIDRs) > 0 {
		clog.Infof(ctx, "Installed nftables FORWARD DROP for service CIDRs %v (family %d)", cfg.ServiceCIDRs, family)
	}
	if len(cfg.PodCIDRs) > 0 {
		clog.Infof(ctx, "Installed nftables FORWARD DROP for pod CIDRs %v leaving via %v (family %d)", cfg.PodCIDRs, cfg.Uplinks, family)
	}
}

// removeSubnetBlackholes tears down the telepresence table for every address
// family. Teardown is safe to call unconditionally, whether or not
// installSubnetBlackholes actually programmed that family.
func removeSubnetBlackholes(ctx context.Context) {
	for _, family := range allFamilies {
		if err := nftutil.Teardown(ctx, routenft.TableName, family); err != nil {
			clog.Debugf(ctx, "Failed to remove nftables FORWARD DROP for family %d: %v", family, err)
		} else {
			clog.Infof(ctx, "Removed nftables FORWARD DROP for family %d", family)
		}
	}
}
