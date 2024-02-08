package cmd

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/cli/values"
	"helm.sh/helm/v3/pkg/getter"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

func helmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "helm",
	}
	cmd.AddCommand(helmInstall(), helmUpgrade(), helmUninstall())
	return cmd
}

type HelmCommand struct {
	values.Options
	helm.Request
	AllValues map[string]any
	rq        *daemon.Request
}

var (
	HelmInstallExtendFlagsFunc func(*pflag.FlagSet)                                      //nolint:gochecknoglobals // extension point
	HelmExtendFlagsFunc        func(*pflag.FlagSet)                                      //nolint:gochecknoglobals // extension point
	HelmInstallPrologFunc      func(context.Context, *pflag.FlagSet, *HelmCommand) error //nolint:gochecknoglobals // extension point
)

func helmInstall() *cobra.Command {
	var upgrade bool

	ha := &HelmCommand{
		Request: helm.Request{
			Type: helm.Install,
		},
	}
	cmd := &cobra.Command{
		Use:   "install",
		Args:  cobra.NoArgs,
		Short: "Install telepresence traffic manager",
		RunE: func(cmd *cobra.Command, args []string) error {
			if upgrade {
				ha.Request.Type = helm.Upgrade
			}
			return ha.run(cmd, args)
		},
	}

	flags := cmd.Flags()
	flags.BoolVarP(&ha.NoHooks, "no-hooks", "", false, "prevent hooks from running during install")
	flags.BoolVarP(&upgrade, "upgrade", "u", false, "replace the traffic manager if it already exists")
	ha.addValueSettingFlags(flags)
	ha.addCRDsFlags(flags)
	uf := flags.Lookup("upgrade")
	uf.Hidden = true
	uf.Deprecated = `Use "telepresence helm upgrade" instead of "telepresence helm install --upgrade"`
	ha.rq = daemon.InitRequest(cmd)
	return cmd
}

func helmUpgrade() *cobra.Command {
	ha := &HelmCommand{
		Request: helm.Request{
			Type: helm.Upgrade,
		},
	}
	cmd := &cobra.Command{
		Use:   "upgrade",
		Args:  cobra.NoArgs,
		Short: "Upgrade telepresence traffic manager",
		RunE:  ha.run,
	}

	flags := cmd.Flags()
	ha.addValueSettingFlags(flags)
	ha.addCRDsFlags(flags)
	flags.BoolVarP(&ha.NoHooks, "no-hooks", "", false, "disable pre/post upgrade hooks")
	flags.BoolVarP(&ha.ResetValues, "reset-values", "", false, "when upgrading, reset the values to the ones built into the chart")
	flags.BoolVarP(&ha.ReuseValues, "reuse-values", "", false,
		"when upgrading, reuse the last release's values and merge in any overrides from the command line via --set and -f")
	ha.rq = daemon.InitRequest(cmd)
	return cmd
}

func (ha *HelmCommand) addValueSettingFlags(flags *pflag.FlagSet) {
	flags.StringArrayVarP(&ha.ValueFiles, "values", "f", []string{},
		"specify values in a YAML file or a URL (can specify multiple)")
	flags.StringArrayVarP(&ha.Values, "set", "", []string{},
		"specify a value as a.b=v (can specify multiple or separate values with commas: a.b=v1,a.c=v2)")
	flags.StringArrayVarP(&ha.FileValues, "set-file", "", []string{},
		"set values from respective files specified via the command line (can specify multiple or separate values with commas: key1=path1,key2=path2)")
	flags.StringArrayVarP(&ha.JSONValues, "set-json", "", []string{},
		"set JSON values on the command line (can specify multiple or separate values with commas: a.b=jsonval1,a.c=jsonval2)")
	flags.StringArrayVarP(&ha.StringValues, "set-string", "", []string{},
		"set STRING values on the command line (can specify multiple or separate values with commas: a.b=val1,a.c=val2)")
	if HelmInstallExtendFlagsFunc != nil {
		HelmInstallExtendFlagsFunc(flags)
	}
}

func (ha *HelmCommand) addCRDsFlags(flags *pflag.FlagSet) {
	if HelmExtendFlagsFunc != nil {
		HelmExtendFlagsFunc(flags)
	}
}

func helmUninstall() *cobra.Command {
	ha := &HelmCommand{
		Request: helm.Request{
			Type: helm.Uninstall,
		},
	}
	cmd := &cobra.Command{
		Use:   "uninstall",
		Args:  cobra.NoArgs,
		Short: "Uninstall telepresence traffic manager",
		RunE:  ha.run,
	}
	flags := cmd.Flags()
	flags.BoolVarP(&ha.NoHooks, "no-hooks", "", false, "prevent hooks from running during uninstallation")
	ha.addCRDsFlags(flags)
	ha.rq = daemon.InitRequest(cmd)
	return cmd
}

func (ha *HelmCommand) Type() helm.RequestType {
	return ha.Request.Type
}

func (ha *HelmCommand) run(cmd *cobra.Command, _ []string) error {
	if ha.ReuseValues && ha.ResetValues {
		return errcat.User.New("--reset-values and --reuse-values are mutually exclusive")
	}
	var err error
	if ha.AllValues, err = ha.MergeValues(getter.All(cli.New())); err != nil {
		return err
	}
	if err = ha.rq.CommitFlags(cmd); err != nil {
		return err
	}
	ctx := cmd.Context()
	if ns, ok := ha.rq.KubeFlags["namespace"]; ok {
		dlog.Debugf(ctx, "using manager namespace %q", ns)
		ha.rq.ManagerNamespace = ns
	}

	ctx = scout.NewReporter(ctx, "cli")
	defer func() {
		if err == nil {
			if ha.Type() == helm.Uninstall {
				scout.Report(ctx, "helm_uninstall_success")
			} else {
				scout.Report(ctx, "helm_install_success", scout.Entry{Key: "upgrade", Value: ha.Type() == helm.Upgrade})
			}
		} else {
			if ha.Type() == helm.Uninstall {
				scout.Report(ctx, "helm_uninstall_failure", scout.Entry{Key: "error", Value: err.Error()})
			} else {
				scout.Report(ctx, "helm_install_failure", scout.Entry{Key: "error", Value: err.Error()}, scout.Entry{Key: "upgrade", Value: ha.Type() == helm.Upgrade})
			}
		}
	}()

	if HelmInstallPrologFunc != nil {
		if err = HelmInstallPrologFunc(ctx, cmd.Flags(), ha); err != nil {
			return err
		}
	}

	ha.ValuesJson, err = json.Marshal(ha.AllValues)
	if err != nil {
		return err
	}

	cr := &ha.rq.ConnectRequest
	var config *client.Kubeconfig
	config, err = client.DaemonKubeconfig(ctx, cr)
	if err != nil {
		return err
	}

	var cluster *k8s.Cluster
	cluster, err = k8s.ConnectCluster(ctx, cr, config)
	if err != nil {
		return err
	}

	if ha.Type() == helm.Uninstall {
		err = helm.DeleteTrafficManager(ctx, cluster.Kubeconfig, cluster.GetManagerNamespace(), false, &ha.Request)
	} else {
		dlog.Debug(ctx, "ensuring that traffic-manager exists")
		err = helm.EnsureTrafficManager(cluster.WithK8sInterface(ctx), cluster.Kubeconfig, cluster.GetManagerNamespace(), &ha.Request)
	}
	if err != nil {
		return err
	}

	var msg string
	switch ha.Type() {
	case helm.Install:
		msg = "installed"
	case helm.Upgrade:
		msg = "upgraded"
	case helm.Uninstall:
		msg = "uninstalled"
	}

	updatedResource := "Traffic Manager"
	if ha.Crds {
		updatedResource = "Telepresence CRDs"
	}

	ioutil.Printf(cmd.OutOrStdout(), "\n%s %s successfully\n", updatedResource, msg)
	return nil
}
