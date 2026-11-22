package cmd

import (
	"maps"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/rbacgen"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/setup"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

func rbacCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rbac",
		Args:  cobra.NoArgs,
		Short: "Generate the RBAC that installing or using Telepresence needs",
	}
	generate := &cobra.Command{
		Use:   "generate",
		Args:  cobra.NoArgs,
		Short: "Generate RBAC manifests for an admin to review and apply",
	}
	generate.AddCommand(rbacGenerateInstall(), rbacGenerateClient())
	cmd.AddCommand(generate)
	return cmd
}

type rbacGenerateInstallCommand struct {
	rq        *daemon.CobraRequest
	inputFile string
	subjects  []string
	verbs     string
	all       bool
}

func rbacGenerateInstall() *cobra.Command {
	gc := &rbacGenerateInstallCommand{}
	cmd := &cobra.Command{
		Use:   "install",
		Args:  cobra.NoArgs,
		Short: "Generate the RBAC that telepresence helm install needs",
		Long: `Generate the RBAC that telepresence helm install needs.

The command renders the embedded chart for the given values, checks what the
identity of the kubeconfig (after --as and --as-group) may not do, and prints a
ClusterRole, Roles and bindings that grant only that. Nothing is applied.

--subject names the subject of the bindings, as User:NAME, Group:NAME or
ServiceAccount:NAMESPACE/NAME. It defaults to the checked identity, and must
match it. --all skips the check and prints the complete set for the given
subjects, without contacting the cluster.`,
		SilenceUsage:      true,
		SilenceErrors:     true,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE:              gc.run,
	}
	flags := cmd.Flags()
	flags.StringVar(&gc.inputFile, "input", "", "Read a Helm values file that overlays the chart defaults")
	flags.StringArrayVar(&gc.subjects, "subject", nil,
		"Subject of the bindings, as User:NAME, Group:NAME or ServiceAccount:NAMESPACE/NAME (repeatable with --all)")
	flags.StringVar(&gc.verbs, "verbs", string(rbacgen.VerbsInstall),
		`What to cover: "install" for a first install, "lifecycle" to also cover upgrades and uninstalls`)
	flags.BoolVar(&gc.all, "all", false, "Print the complete set without checking the cluster")
	_ = cmd.MarkFlagFilename("input")
	gc.rq = daemon.InitKubeRequest(cmd)
	return cmd
}

func (gc *rbacGenerateInstallCommand) run(cmd *cobra.Command, _ []string) error {
	if err := gc.rq.CommitFlags(cmd); err != nil {
		return err
	}
	ctx := cmd.Context()
	verbs, err := rbacgen.ParseVerbs(gc.verbs)
	if err != nil {
		return errcat.User.New(err)
	}
	subjects := make([]rbacgen.Subject, len(gc.subjects))
	for i, s := range gc.subjects {
		if subjects[i], err = rbacgen.ParseSubject(s); err != nil {
			return errcat.User.New(err)
		}
	}
	values := &helm.Values{}
	if gc.inputFile != "" {
		if values, err = setup.LoadInputValues(gc.inputFile); err != nil {
			return err
		}
	}

	cr := gc.rq.ConnectRequest
	rq := &rbacgen.InstallRequest{
		Values:       values,
		ValuesSource: gc.inputFile,
		Subjects:     subjects,
		Verbs:        verbs,
		All:          gc.all,
		Impersonated: cr.KubeFlags["as"] != "",
	}
	if gc.all {
		rq.ManagerNamespace = cr.ManagerNamespace
		if rq.ManagerNamespace == "" {
			rq.ManagerNamespace = k8s.GetManagerNamespace(ctx)
		}
		if rq.ManagerNamespace == "" {
			rq.ManagerNamespace = "ambassador"
		}
	} else {
		config, err := k8s.DaemonKubeconfig(ctx, cr)
		if err != nil {
			return err
		}
		cluster, err := k8s.ConnectCluster(cr, config)
		if err != nil {
			return err
		}
		restCfg, err := cluster.ToRESTConfig()
		if err != nil {
			return err
		}
		// The sweep issues many parallel SelfSubjectAccessReviews.
		restCfg.QPS = 50
		restCfg.Burst = 100
		if rq.Client, err = kubernetes.NewForConfig(restCfg); err != nil {
			return err
		}
		rq.ManagerNamespace = k8s.GetManagerNamespace(cluster)
	}

	out, err := rbacgen.GenerateInstall(ctx, rq)
	if err != nil {
		return err
	}
	_, err = out.WriteTo(cmd.OutOrStdout())
	return err
}

type rbacGenerateClientCommand struct {
	rq         *daemon.CobraRequest
	inputFile  string
	subjects   []string
	namespaces []string
	emit       string
}

func rbacGenerateClient() *cobra.Command {
	gc := &rbacGenerateClientCommand{}
	cmd := &cobra.Command{
		Use:   "client",
		Args:  cobra.NoArgs,
		Short: "Generate the RBAC that a Telepresence client needs",
		Long: `Generate the RBAC that a Telepresence client needs.

The command renders the chart's client roles for the configuration the
traffic-manager is installed with, and prints them for the given subjects.
Nothing is applied.

The configuration is read from the traffic-manager release with your own
credentials, also when --as is given, which needs read access to the Helm
release secrets in the manager namespace. Without that access, or for an install that doesn't
exist yet, pass the values with --input.

--subject names the subject of the bindings, as User:NAME, Group:NAME or
ServiceAccount:NAMESPACE/NAME. --namespace limits the rules to the given
namespaces, which the traffic-manager must manage. --emit values prints a
clientRbac values block for the release instead of manifests.`,
		SilenceUsage:      true,
		SilenceErrors:     true,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE:              gc.run,
	}
	flags := cmd.Flags()
	flags.StringVar(&gc.inputFile, "input", "", "Read the values the traffic-manager is installed with from a Helm values file")
	flags.StringArrayVar(&gc.subjects, "subject", nil,
		"Subject of the bindings, as User:NAME, Group:NAME or ServiceAccount:NAMESPACE/NAME (repeatable)")
	flags.StringArrayVar(&gc.namespaces, "namespace", nil, "Namespace the client may use (repeatable, default every managed namespace)")
	flags.StringVar(&gc.emit, "emit", string(rbacgen.FormatRBAC), `What to print: "rbac" for manifests, "values" for a clientRbac values block`)
	_ = cmd.MarkFlagFilename("input")
	_ = cmd.MarkFlagRequired("subject")
	gc.rq = daemon.InitKubeRequest(cmd)
	return cmd
}

func (gc *rbacGenerateClientCommand) run(cmd *cobra.Command, _ []string) error {
	if err := gc.rq.CommitFlags(cmd); err != nil {
		return err
	}
	ctx := cmd.Context()
	format, err := rbacgen.ParseClientFormat(gc.emit)
	if err != nil {
		return errcat.User.New(err)
	}
	subjects := make([]rbacgen.Subject, len(gc.subjects))
	for i, s := range gc.subjects {
		if subjects[i], err = rbacgen.ParseSubject(s); err != nil {
			return errcat.User.New(err)
		}
	}
	rq := &rbacgen.ClientRequest{
		Subjects:     subjects,
		Namespaces:   gc.namespaces,
		ValuesSource: gc.inputFile,
		Format:       format,
	}
	if gc.inputFile != "" {
		if rq.Values, err = setup.LoadInputValues(gc.inputFile); err != nil {
			return err
		}
	}

	// Nothing here is reviewed, so the cluster is read with the caller's own
	// credentials, also when --as is given.
	cr := proto.Clone(gc.rq.ConnectRequest).(*connector.ConnectRequest)
	cr.KubeFlags = maps.Clone(cr.KubeFlags)
	for _, f := range []string{"as", "as-group", "as-uid"} {
		delete(cr.KubeFlags, f)
	}
	config, err := k8s.DaemonKubeconfig(ctx, cr)
	if err != nil {
		return err
	}
	cluster, err := k8s.ConnectCluster(cr, config)
	if err != nil {
		return err
	}
	restCfg, err := cluster.ToRESTConfig()
	if err != nil {
		return err
	}
	if rq.Client, err = kubernetes.NewForConfig(restCfg); err != nil {
		return err
	}
	rq.ManagerNamespace = k8s.GetManagerNamespace(cluster)
	if rq.Values == nil {
		rq.ReleaseLookup = setup.NewReleaseLookup(cluster.Kubeconfig)
	}

	out, err := rbacgen.GenerateClient(ctx, rq)
	if err != nil {
		return err
	}
	_, err = out.WriteTo(cmd.OutOrStdout())
	return err
}
