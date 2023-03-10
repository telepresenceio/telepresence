package cli

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/cli/values"
	"helm.sh/helm/v3/pkg/getter"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

func helmCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "helm",
	}
	cmd.AddCommand(helmInstallCommand(), helmUpgradeCommand(), helmUninstallCommand())
	return cmd
}

type HelmOpts struct {
	values.Options
	AllValues   map[string]any
	ReuseValues bool
	ResetValues bool
	Request     *daemon.Request
	cmdType     connector.HelmRequest_Type
	CRDs        bool
}

var (
	HelmInstallExtendFlagsFunc func(*pflag.FlagSet)                                   //nolint:gochecknoglobals // extension point
	HelmExtendFlagsFunc        func(*pflag.FlagSet)                                   //nolint:gochecknoglobals // extension point
	HelmInstallPrologFunc      func(context.Context, *pflag.FlagSet, *HelmOpts) error //nolint:gochecknoglobals // extension point
)

func helmInstallCommand() *cobra.Command {
	var upgrade bool

	ha := &HelmOpts{
		cmdType: connector.HelmRequest_INSTALL,
	}
	cmd := &cobra.Command{
		Use:   "install",
		Args:  cobra.NoArgs,
		Short: "Install telepresence traffic manager",
		RunE: func(cmd *cobra.Command, args []string) error {
			if upgrade {
				ha.cmdType = connector.HelmRequest_UPGRADE
			}
			return ha.run(cmd, args)
		},
		Annotations: map[string]string{
			ann.UserDaemon:   ann.Required,
			ann.VersionCheck: ann.Required,
		},
	}

	flags := cmd.Flags()
	flags.BoolVarP(&upgrade, "upgrade", "u", false, "replace the traffic manager if it already exists")
	ha.addValueSettingFlags(flags)
	ha.addCRDsFlags(flags)
	uf := flags.Lookup("upgrade")
	uf.Hidden = true
	uf.Deprecated = `Use "telepresence helm upgrade" instead of "telepresence helm install --upgrade"`
	ha.Request = daemon.InitRequest(cmd)
	return cmd
}

func helmUpgradeCommand() *cobra.Command {
	ha := &HelmOpts{
		cmdType: connector.HelmRequest_UPGRADE,
	}
	cmd := &cobra.Command{
		Use:   "upgrade",
		Args:  cobra.NoArgs,
		Short: "Upgrade telepresence traffic manager",
		RunE:  ha.run,
		Annotations: map[string]string{
			ann.UserDaemon:   ann.Required,
			ann.VersionCheck: ann.Required,
		},
	}

	flags := cmd.Flags()
	ha.addValueSettingFlags(flags)
	ha.addCRDsFlags(flags)
	flags.BoolVarP(&ha.ResetValues, "reset-values", "", false,
		"when upgrading, reset the values to the ones built into the chart")
	flags.BoolVarP(&ha.ReuseValues, "reuse-values", "", false,
		"when upgrading, reuse the last release's values and merge in any overrides from the command line via --set and -f")
	ha.Request = daemon.InitRequest(cmd)
	return cmd
}

func (ha *HelmOpts) addValueSettingFlags(flags *pflag.FlagSet) {
	flags.StringSliceVarP(&ha.ValueFiles, "values", "f", []string{},
		"specify values in a YAML file or a URL (can specify multiple)")
	flags.StringSliceVarP(&ha.Values, "set", "", []string{},
		"specify a value as a.b=v (can specify multiple or separate values with commas: a.b=v1,a.c=v2)")
	flags.StringSliceVarP(&ha.FileValues, "set-file", "", []string{},
		"set values from respective files specified via the command line (can specify multiple or separate values with commas: key1=path1,key2=path2)")
	flags.StringSliceVarP(&ha.JSONValues, "set-json", "", []string{},
		"set JSON values on the command line (can specify multiple or separate values with commas: a.b=jsonval1,a.c=jsonval2)")
	flags.StringSliceVarP(&ha.StringValues, "set-string", "", []string{},
		"set STRING values on the command line (can specify multiple or separate values with commas: a.b=val1,a.c=val2)")
	if HelmInstallExtendFlagsFunc != nil {
		HelmInstallExtendFlagsFunc(flags)
	}
}

func (ha *HelmOpts) addCRDsFlags(flags *pflag.FlagSet) {
	if HelmExtendFlagsFunc != nil {
		HelmExtendFlagsFunc(flags)
	}
}

func helmUninstallCommand() *cobra.Command {
	ha := &HelmOpts{
		cmdType: connector.HelmRequest_UNINSTALL,
	}
	cmd := &cobra.Command{
		Use:   "uninstall",
		Args:  cobra.NoArgs,
		Short: "Uninstall telepresence traffic manager",
		RunE:  ha.run,
		Annotations: map[string]string{
			ann.UserDaemon:   ann.Required,
			ann.VersionCheck: ann.Required,
		},
	}
	ha.addCRDsFlags(cmd.Flags())
	ha.Request = daemon.InitRequest(cmd)
	return cmd
}

func (ha *HelmOpts) Type() connector.HelmRequest_Type {
	return ha.cmdType
}

func (ha *HelmOpts) run(cmd *cobra.Command, _ []string) error {
	if ha.ReuseValues && ha.ResetValues {
		return errcat.User.New("--reset-values and --reuse-values are mutually exclusive")
	}
	var err error
	if ha.AllValues, err = ha.MergeValues(getter.All(cli.New())); err != nil {
		return err
	}
	ha.Request.CommitFlags(cmd)

	if err = util.InitCommand(cmd); err != nil {
		return err
	}
	ctx := cmd.Context()
	if HelmInstallPrologFunc != nil {
		if err := HelmInstallPrologFunc(ctx, cmd.Flags(), ha); err != nil {
			return err
		}
	}

	valuesJSON, err := json.Marshal(ha.AllValues)
	if err != nil {
		return err
	}

	request := &connector.HelmRequest{
		Type:           ha.cmdType,
		ValuesJson:     valuesJSON,
		ReuseValues:    ha.ReuseValues,
		ResetValues:    ha.ResetValues,
		ConnectRequest: &ha.Request.ConnectRequest,
		Crds:           ha.CRDs,
	}
	ud := daemon.GetUserClient(ctx)
	if ud.Remote && daemon.GetSession(ctx) == nil {
		// This is needed here, because we never establish a session.
		if err := docker.EnableK8SAuthenticator(ctx); err != nil {
			return err
		}
	}
	resp, err := ud.Helm(ctx, request)
	if err != nil {
		return err
	}
	if err = errcat.FromResult(resp); err != nil {
		return err
	}

	var msg string
	switch ha.cmdType {
	case connector.HelmRequest_INSTALL:
		msg = "installed"
	case connector.HelmRequest_UPGRADE:
		msg = "upgraded"
	case connector.HelmRequest_UNINSTALL:
		msg = "uninstalled"
	}

	updatedResource := "Traffic Manager"
	if ha.CRDs {
		updatedResource = "Telepresence CRDs"
	}

	ioutil.Printf(cmd.OutOrStdout(), "\n%s %s successfully\n", updatedResource, msg)
	return nil
}
