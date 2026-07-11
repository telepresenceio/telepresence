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

// installSubnetBlackholes builds and applies a native nftables ruleset (see
// pkg/routenft) that drops FORWARD-chain traffic destined for the discovered
// service CIDRs -- one ruleset per address family that has at least one CIDR.
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
func installSubnetBlackholes(ctx context.Context, cs *kubernetes.Clientset) {
	var v4, v6 []netip.Prefix
	for _, cidr := range discoverServiceCIDRs(ctx, cs) {
		cidr = strings.TrimSpace(cidr)
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			clog.Errorf(ctx, "Failed to parse service CIDR %q: %v", cidr, err)
			continue
		}
		prefix = prefix.Masked()
		if prefix.Addr().Is4() {
			v4 = append(v4, prefix)
		} else {
			v6 = append(v6, prefix)
		}
	}

	applyFamily(ctx, nftables.TableFamilyIPv4, v4)
	applyFamily(ctx, nftables.TableFamilyIPv6, v6)
}

// applyFamily builds and applies the routenft ruleset for family's CIDRs. It
// is a no-op when cidrs is empty, mirroring the previous behaviour of never
// creating a rule for a family with no discovered service CIDRs.
func applyFamily(ctx context.Context, family nftables.TableFamily, cidrs []netip.Prefix) {
	if len(cidrs) == 0 {
		clog.Debugf(ctx, "No service CIDRs for family %d; nftables blackhole not installed", family)
		return
	}
	rs, err := routenft.Build(routenft.Config{Family: family, CIDRs: cidrs})
	if err != nil {
		clog.Errorf(ctx, "Failed to build nftables ruleset for family %d: %v", family, err)
		return
	}
	if err := nftutil.Apply(ctx, &rs.Ruleset); err != nil {
		clog.Errorf(ctx, "Failed to apply nftables FORWARD DROP for family %d: %v", family, err)
		return
	}
	clog.Infof(ctx, "Installed nftables FORWARD DROP for service CIDRs %v (family %d)", cidrs, family)
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
