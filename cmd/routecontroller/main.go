package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/coreos/go-iptables/iptables"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/telepresenceio/clog"
	tplog "github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/sigctx"
)

// subnetRule tracks an iptables FORWARD DROP rule installed for a service CIDR.
type subnetRule struct {
	ipt  *iptables.IPTables
	cidr string
}

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

	rules := installSubnetBlackholes(ctx, cs)
	defer removeSubnetBlackholes(ctx, rules)

	clog.Info(ctx, "Route controller started")
	<-ctx.Done()
	return nil
}

// discoverServiceCIDRs returns the service CIDRs for which iptables FORWARD DROP rules
// will be installed. It first checks the SERVICE_CIDRS environment variable
// (comma-separated list of CIDRs). If that is not set, it queries the Kubernetes
// ServiceCIDR API (available in k8s 1.33+). If neither is available it returns nil and
// no subnet-level protection is installed; set SERVICE_CIDRS explicitly in that case.
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

// installSubnetBlackholes installs iptables FORWARD chain DROP rules for each service CIDR.
//
// An iptables rule in the FORWARD chain (rather than a kernel blackhole route) is used
// because RTN_BLACKHOLE routes fail connect()/sendmsg() at the socket level before
// any iptables hook can fire, which breaks locally-generated traffic such as
// kube-apiserver → mutating-webhook calls.
//
// The FORWARD chain only affects traffic forwarded through the host (i.e. pod traffic via
// veth pairs). Locally-generated host traffic is never subject to the FORWARD chain.
//
// For active services, kube-proxy's PREROUTING DNAT fires before the FORWARD chain and
// rewrites the destination from ClusterIP to a pod IP, so the DROP rule (which matches
// on the original service CIDR) does not apply. For deleted or never-assigned ClusterIPs
// no DNAT rule exists, the FORWARD chain sees the original ClusterIP, and the DROP fires.
func installSubnetBlackholes(ctx context.Context, cs *kubernetes.Clientset) []subnetRule {
	var rules []subnetRule
	for _, cidr := range discoverServiceCIDRs(ctx, cs) {
		cidr = strings.TrimSpace(cidr)
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			clog.Errorf(ctx, "Failed to parse service CIDR %q: %v", cidr, err)
			continue
		}

		proto := iptables.ProtocolIPv4
		if network.IP.To4() == nil {
			proto = iptables.ProtocolIPv6
		}

		ipt, err := iptables.NewWithProtocol(proto)
		if err != nil {
			clog.Errorf(ctx, "Failed to initialise iptables for %s: %v", network, err)
			continue
		}

		exists, err := ipt.Exists("filter", "FORWARD", "-d", network.String(), "-j", "DROP")
		if err != nil {
			clog.Errorf(ctx, "Failed to check iptables FORWARD rule for %s: %v", network, err)
			continue
		}
		if !exists {
			if err := ipt.Insert("filter", "FORWARD", 1, "-d", network.String(), "-j", "DROP"); err != nil {
				clog.Errorf(ctx, "Failed to add iptables FORWARD DROP for service CIDR %s: %v", network, err)
				continue
			}
			clog.Infof(ctx, "Added iptables FORWARD DROP for service CIDR %s", network)
		} else {
			clog.Debugf(ctx, "iptables FORWARD DROP for %s already present", network)
		}

		rules = append(rules, subnetRule{ipt: ipt, cidr: network.String()})
	}
	return rules
}

func removeSubnetBlackholes(ctx context.Context, rules []subnetRule) {
	for _, rule := range rules {
		if err := rule.ipt.Delete("filter", "FORWARD", "-d", rule.cidr, "-j", "DROP"); err != nil {
			clog.Debugf(ctx, "Failed to remove iptables FORWARD DROP for %s: %v", rule.cidr, err)
		} else {
			clog.Infof(ctx, "Removed iptables FORWARD DROP for service CIDR %s", rule.cidr)
		}
	}
}
