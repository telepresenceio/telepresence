package manifest

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"net/netip"
	"os"
	"regexp"
	"slices"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	daemonrpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// buildConnectRequest turns a manifest Connection into a daemon.Request, replicating what
// daemon.CobraRequest.Commit does for cobra-parsed flags, since a manifest-driven connection
// never goes through cobra flag parsing.
func buildConnectRequest(mc *Connection) (*daemon.Request, error) {
	cr := &daemon.Request{
		ConnectRequest: &connector.ConnectRequest{
			KubeFlags: make(map[string]string, len(mc.KubeFlags)+3),
		},
	}
	maps.Copy(cr.KubeFlags, mc.KubeFlags)
	if mc.Context != "" {
		cr.KubeFlags["context"] = mc.Context
	}
	if mc.Namespace != "" {
		cr.KubeFlags["namespace"] = mc.Namespace
	}
	if mc.Kubeconfig != "" {
		cr.KubeFlags["kubeconfig"] = mc.Kubeconfig
	}
	cr.Name = mc.Name
	cr.ManagerNamespace = mc.ManagerNamespace
	cr.MappedNamespaces = slices.Clone(mc.MappedNamespaces)
	cr.AlsoProxy = slices.Clone(mc.AlsoProxy)
	cr.NeverProxy = slices.Clone(mc.NeverProxy)
	cr.AllowConflictingSubnets = slices.Clone(mc.AllowConflictingSubnets)
	cr.LocalReroutes = slices.Clone(mc.RerouteLocal)
	cr.RemoteReroutes = slices.Clone(mc.RerouteRemote)

	svs, err := buildSubnetViaWorkloads(mc.ProxyVia)
	if err != nil {
		return nil, err
	}
	cr.SubnetViaWorkloads = svs
	addKubeconfigEnv(cr)
	return cr, nil
}

// addKubeconfigEnv mirrors the unexported daemon.Request.addKubeconfigEnv: the KUBECONFIG and
// GOOGLE_APPLICATION_CREDENTIALS environment variables must reach the connector daemon process
// explicitly, because that's a separate process that doesn't inherit the CLI's environment.
func addKubeconfigEnv(cr *daemon.Request) {
	cr.Environment = make(map[string]string, 2)
	addEnv := func(key string) {
		if v, ok := os.LookupEnv(key); ok {
			cr.Environment[key] = v
		} else {
			cr.Environment["-"+key] = ""
		}
	}
	addEnv("KUBECONFIG")
	addEnv("GOOGLE_APPLICATION_CREDENTIALS")
}

type subnetOrSymbol struct {
	prefix   netip.Prefix
	symbolic string
	workload string
}

// buildSubnetViaWorkloads mirrors the symbolic-subnet handling and overlap validation of
// daemon.parseProxyVias, applied to the manifest's structured ProxyVia entries instead of the
// "CIDR=WORKLOAD" flag strings.
func buildSubnetViaWorkloads(pvs []ProxyVia) ([]*daemonrpc.SubnetViaWorkload, error) {
	if len(pvs) == 0 {
		return nil, nil
	}
	entries := make([]subnetOrSymbol, 0, len(pvs))
	overlaps := func(e subnetOrSymbol) error {
		for _, o := range entries {
			switch {
			case e.symbolic != "" && o.symbolic == e.symbolic:
				return errcat.User.Newf("proxyVia entries for %q are overlapping", e.symbolic)
			case e.symbolic == "" && o.symbolic == "" && o.prefix.Overlaps(e.prefix):
				return errcat.User.Newf("proxyVia subnets %s and %s are overlapping", o.prefix, e.prefix)
			}
		}
		return nil
	}
	for _, pv := range pvs {
		var e subnetOrSymbol
		switch pv.Subnet {
		case "service", "pods", "also":
			e.symbolic = pv.Subnet
		case "all":
			e.symbolic = "all"
		default:
			p, err := netip.ParsePrefix(pv.Subnet)
			if err != nil {
				return nil, errcat.User.Errorf(err, "proxyVia subnet %q is not a valid CIDR", pv.Subnet)
			}
			e.prefix = p
		}
		e.workload = pv.Workload
		if e.symbolic == "all" {
			for _, sym := range []string{"also", "pods", "service"} {
				se := subnetOrSymbol{symbolic: sym, workload: e.workload}
				if err := overlaps(se); err != nil {
					return nil, err
				}
				entries = append(entries, se)
			}
			continue
		}
		if err := overlaps(e); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	svs := make([]*daemonrpc.SubnetViaWorkload, len(entries))
	for i, e := range entries {
		n := e.symbolic
		if n == "" {
			n = e.prefix.String()
		}
		svs[i] = &daemonrpc.SubnetViaWorkload{Subnet: n, Workload: e.workload}
	}
	return svs, nil
}

// requireCurrentSession makes the currently selected connection available in the command's
// context. A manifest without a connection of its own must never connect implicitly, so a missing
// session is a User error rather than the deprecated implicit connect that other session-bound
// commands still perform.
func requireCurrentSession(cmd *cobra.Command) error {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[ann.Session] = ann.Optional
	if err := connect.InitCommand(cmd); err != nil {
		return err
	}
	if daemon.GetSession(cmd.Context()) == nil {
		return errcat.User.New(`not connected; run "telepresence connect" first, or declare the connection in the manifest`)
	}
	return nil
}

// findConnectionInfo looks up the running daemon whose identity matches id, without dialing it
// and without launching one. A nil *daemon.Info with a nil error means no matching daemon is
// currently running.
func findConnectionInfo(ctx context.Context, id *daemon.Identifier) (*daemon.Info, error) {
	il := daemon.NewUserInfoLoader(ctx)
	match := regexp.MustCompile(`\A` + regexp.QuoteMeta(id.Name) + `\z`)
	info, err := il.LoadMatchingInfo(match)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return info, nil
}
