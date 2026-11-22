package cmd

import (
	"context"
	"io"
	"maps"
	"slices"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/rbacgen"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/setup"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

func rbacCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rbac",
		Args:  cobra.NoArgs,
		Short: "Generate or check the RBAC that installing or using Telepresence needs",
	}
	generate := &cobra.Command{
		Use:   "generate",
		Args:  cobra.NoArgs,
		Short: "Generate RBAC manifests for an admin to review and apply",
	}
	generate.AddCommand(rbacGenerateInstall(), rbacGenerateClient())
	verify := &cobra.Command{
		Use:   "verify",
		Args:  cobra.NoArgs,
		Short: "Check that an identity holds the RBAC that installing or using Telepresence needs",
	}
	verify.AddCommand(rbacVerifyInstall(), rbacVerifyClient())
	cmd.AddCommand(generate, verify)
	return cmd
}

// kubeClient connects to the cluster of cr and returns a client for it.
func kubeClient(ctx context.Context, cr *connector.ConnectRequest) (*k8s.Cluster, kubernetes.Interface, error) {
	config, err := k8s.DaemonKubeconfig(ctx, cr)
	if err != nil {
		return nil, nil, err
	}
	cluster, err := k8s.ConnectCluster(cr, config)
	if err != nil {
		return nil, nil, err
	}
	restCfg, err := cluster.ToRESTConfig()
	if err != nil {
		return nil, nil, err
	}
	// A sweep issues many parallel SelfSubjectAccessReviews.
	restCfg = rest.CopyConfig(restCfg)
	restCfg.QPS = 50
	restCfg.Burst = 100
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, nil, err
	}
	return cluster, client, nil
}

// callerRequest returns cr without the impersonation flags, for reads that
// use the caller's own credentials.
func callerRequest(cr *connector.ConnectRequest) *connector.ConnectRequest {
	cr = proto.Clone(cr).(*connector.ConnectRequest)
	cr.KubeFlags = maps.Clone(cr.KubeFlags)
	for _, f := range []string{"as", "as-group", "as-uid", "as-user-extra"} {
		delete(cr.KubeFlags, f)
	}
	return cr
}

// withManagerNamespace returns a copy of cr that names the manager namespace the way
// telepresence helm install does: --manager-namespace, else --namespace, else ambassador.
// Naming it up front keeps k8s.NewCluster from discovering it, which an identity without
// rights could not do.
func withManagerNamespace(cr *connector.ConnectRequest) *connector.ConnectRequest {
	cr = proto.Clone(cr).(*connector.ConnectRequest)
	cr.ManagerNamespace = helm.ManagerNamespace(cr)
	return cr
}

// parseSubjects parses the values of --subject.
func parseSubjects(ss []string) ([]rbacgen.Subject, error) {
	subjects := make([]rbacgen.Subject, len(ss))
	for i, s := range ss {
		var err error
		if subjects[i], err = rbacgen.ParseSubject(s); err != nil {
			return nil, errcat.User.New(err)
		}
	}
	return subjects, nil
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
subjects, without contacting the cluster.

The traffic-manager namespace is --manager-namespace, or ambassador when it's
not given. These commands have no -n, so if you install with
telepresence helm install -n foo, pass --manager-namespace foo here. It isn't
looked up in the cluster.`,
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
	subjects, err := parseSubjects(gc.subjects)
	if err != nil {
		return err
	}
	values := &helm.Values{}
	if gc.inputFile != "" {
		if values, err = setup.LoadInputValues(gc.inputFile); err != nil {
			return err
		}
	}

	cr := withManagerNamespace(gc.rq.ConnectRequest)
	rq := &rbacgen.InstallRequest{
		Values:       values,
		ValuesSource: gc.inputFile,
		Subjects:     subjects,
		Verbs:        verbs,
		All:          gc.all,
		Impersonated: cr.KubeFlags["as"] != "",
	}
	rq.ManagerNamespace = cr.ManagerNamespace
	if !gc.all {
		_, client, err := kubeClient(ctx, cr)
		if err != nil {
			return err
		}
		rq.Client = client
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
	subjects, err := parseSubjects(gc.subjects)
	if err != nil {
		return err
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
	cluster, client, err := kubeClient(ctx, callerRequest(gc.rq.ConnectRequest))
	if err != nil {
		return err
	}
	rq.Client = client
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

type rbacVerifyInstallCommand struct {
	rq        *daemon.CobraRequest
	inputFile string
	verbs     string
}

func rbacVerifyInstall() *cobra.Command {
	vc := &rbacVerifyInstallCommand{}
	cmd := &cobra.Command{
		Use:   "install",
		Args:  cobra.NoArgs,
		Short: "Check that an identity may run telepresence helm install",
		Long: `Check that an identity may run telepresence helm install.

The command renders the embedded chart for the given values, checks the same
permissions as telepresence rbac generate install for the identity of the
kubeconfig (after --as and --as-group), and prints what's missing. It exits
with a non-zero status when anything is missing.

The traffic-manager namespace is --manager-namespace, or ambassador when it's
not given. These commands have no -n, so if you install with
telepresence helm install -n foo, pass --manager-namespace foo here. It isn't
looked up in the cluster.`,
		SilenceUsage:      true,
		SilenceErrors:     true,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE:              vc.run,
	}
	flags := cmd.Flags()
	flags.StringVar(&vc.inputFile, "input", "", "Read a Helm values file that overlays the chart defaults")
	flags.StringVar(&vc.verbs, "verbs", string(rbacgen.VerbsInstall),
		`What to check: "install" for a first install, "lifecycle" to also check upgrades and uninstalls`)
	_ = cmd.MarkFlagFilename("input")
	vc.rq = daemon.InitKubeRequest(cmd)
	return cmd
}

func (vc *rbacVerifyInstallCommand) run(cmd *cobra.Command, _ []string) error {
	if err := vc.rq.CommitFlags(cmd); err != nil {
		return err
	}
	ctx := cmd.Context()
	verbs, err := rbacgen.ParseVerbs(vc.verbs)
	if err != nil {
		return errcat.User.New(err)
	}
	rq := &rbacgen.VerifyInstallRequest{Values: &helm.Values{}, ValuesSource: vc.inputFile, Verbs: verbs}
	if vc.inputFile != "" {
		if rq.Values, err = setup.LoadInputValues(vc.inputFile); err != nil {
			return err
		}
	}
	cr := withManagerNamespace(vc.rq.ConnectRequest)
	_, client, err := kubeClient(ctx, cr)
	if err != nil {
		return err
	}
	rq.Client = client
	rq.ManagerNamespace = cr.ManagerNamespace

	v, err := rbacgen.VerifyInstall(ctx, rq)
	if err != nil {
		return err
	}
	return reportVerdict(cmd, v)
}

type rbacVerifyClientCommand struct {
	rq         *daemon.CobraRequest
	inputFile  string
	namespaces []string
	workloads  []string
}

func rbacVerifyClient() *cobra.Command {
	vc := &rbacVerifyClientCommand{}
	cmd := &cobra.Command{
		Use:   "client",
		Args:  cobra.NoArgs,
		Short: "Check that an identity may use Telepresence",
		Long: `Check that an identity may use Telepresence.

The command runs the traffic-manager's own authorization reviews for the
identity of the kubeconfig (after --as and --as-group): connecting, reading a
namespace, intercepting and ingesting, and getting logs. An operation counts
as missing only when none of the grants the traffic-manager accepts for it
passes. When the traffic-manager publishes no external endpoint, the
port-forward to the traffic-manager pod is checked as well.

The required grant and the external endpoint are read from the
traffic-manager release with your own credentials, also when --as is given.
When the release can't be read, the checks run under every required grant,
unless --input gives the values the traffic-manager is installed with, and
the command passes when they all pass under one of them.

Without --namespace, the namespace of the kubeconfig context is checked.
Grants scoped to single workloads are only seen with --workload, and a
workload that doesn't exist is an error. The command exits with a non-zero
status when a required check is missing or can't be determined.`,
		SilenceUsage:      true,
		SilenceErrors:     true,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE:              vc.run,
	}
	flags := cmd.Flags()
	flags.StringVar(&vc.inputFile, "input", "", "Read the values the traffic-manager is installed with from a Helm values file")
	flags.StringArrayVar(&vc.namespaces, "namespace", nil, "Namespace to check (repeatable, default the namespace of the kubeconfig context)")
	flags.StringArrayVar(&vc.workloads, "workload", nil, "Workload to run the per-workload reviews for, in every checked namespace (repeatable)")
	_ = cmd.MarkFlagFilename("input")
	vc.rq = daemon.InitKubeRequest(cmd)
	return cmd
}

func (vc *rbacVerifyClientCommand) run(cmd *cobra.Command, _ []string) error {
	if err := vc.rq.CommitFlags(cmd); err != nil {
		return err
	}
	ctx := cmd.Context()
	rq := &rbacgen.VerifyClientRequest{
		Workloads:    slices.Compact(slices.Sorted(slices.Values(vc.workloads))),
		Namespaces:   slices.Compact(slices.Sorted(slices.Values(vc.namespaces))),
		ValuesSource: vc.inputFile,
	}
	var err error
	if vc.inputFile != "" {
		if rq.Values, err = setup.LoadInputValues(vc.inputFile); err != nil {
			return err
		}
	}

	// Only the reviews are impersonated. The manager namespace, the release
	// and the pods are found with the caller's own credentials. The namespace
	// is found first, so that connecting as the checked identity doesn't try
	// to discover it.
	callerCluster, callerClient, err := kubeClient(ctx, callerRequest(vc.rq.ConnectRequest))
	if err != nil {
		return err
	}
	rq.ManagerNamespace = k8s.GetManagerNamespace(callerCluster)
	cr := proto.Clone(vc.rq.ConnectRequest).(*connector.ConnectRequest)
	cr.ManagerNamespace = rq.ManagerNamespace
	cluster, client, err := kubeClient(ctx, cr)
	if err != nil {
		return err
	}
	rq.Client = client
	callerCfg, err := callerCluster.ToRESTConfig()
	if err != nil {
		return err
	}
	rollouts, err := argorollouts.NewForConfig(callerCfg)
	if err != nil {
		return err
	}
	rq.WorkloadPods = rbacgen.WorkloadPodNames(callerClient, rollouts)
	if rq.Values == nil {
		rq.ReleaseLookup = setup.NewReleaseLookup(callerCluster.Kubeconfig)
	}
	if len(rq.Namespaces) == 0 {
		ns := cluster.Namespace
		if ns == "" {
			ns = "default"
		}
		rq.Namespaces = []string{ns}
	}

	v, err := rbacgen.VerifyClient(ctx, rq)
	if err != nil {
		return err
	}
	return reportVerdict(cmd, v)
}

type verdict interface {
	OK() bool
	WriteText(io.Writer)
}

// reportVerdict prints v, as an object when formatted output is requested,
// and returns a Silent error when something is missing, so that the
// command exits with a non-zero status.
func reportVerdict(cmd *cobra.Command, v verdict) error {
	if output.WantsFormatted(cmd) {
		output.Report(cmd.Context(), v)
	} else {
		v.WriteText(cmd.OutOrStdout())
	}
	if !v.OK() {
		return errcat.Silent.New("permissions are missing")
	}
	return nil
}
