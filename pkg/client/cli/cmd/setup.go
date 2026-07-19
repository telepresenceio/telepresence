package cmd

import (
	"io"

	"github.com/moby/term"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/setup"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

type setupCommand struct {
	rq             *daemon.CobraRequest
	nonInteractive bool
	attach         bool
	replace        bool
	upgradeManager bool
	quic           string
	nodeAgent      string
	scope          string
	managedNss     []string
	outputFile     string
	inputFile      string
	apply          bool
}

func setupCmd() *cobra.Command {
	sc := &setupCommand{}
	cmd := &cobra.Command{
		Use:   "setup",
		Args:  cobra.NoArgs,
		Short: "Analyze the cluster and propose or apply a traffic-manager configuration",
		Long: `Analyze the cluster and propose or apply a traffic-manager configuration.

The command probes the cluster (privileges, QUIC viability, node-agent
viability, webhook creation, namespace scale, and any existing installation),
asks a small number of questions that the findings make relevant, and prints a
report with a generated Helm values document. Without --output or --apply the
command only validates the setup; --output writes the values file, and --apply
installs or upgrades the traffic-manager with it.`,
		Annotations: map[string]string{
			ann.UpdateCheckFormat: ann.Tel2,
		},
		SilenceUsage:      true,
		SilenceErrors:     true,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE:              sc.run,
	}
	flags := cmd.Flags()
	flags.StringVar(&sc.outputFile, "output", "",
		`Write the resulting Helm values to this file, suitable for a Helm install; "-" writes them to stdout and suppresses the report`)
	flags.StringVar(&sc.inputFile, "input", "", "Read a Helm values file; its settings become pinned defaults")
	flags.BoolVar(&sc.apply, "apply", false, "Install/upgrade the traffic-manager with the resulting values")
	flags.BoolVar(&sc.nonInteractive, "non-interactive", false,
		"Never prompt; unanswered questions fall back to flag values, input-pinned settings, or safe defaults")
	flags.BoolVar(&sc.attach, "attach", true, "Clients will attach to workloads (intercept/replace/ingest/wiretap)")
	flags.BoolVar(&sc.replace, "replace", false, "The replace command will be used")
	flags.BoolVar(&sc.upgradeManager, "upgrade-manager", true, "Upgrade an existing, older traffic-manager")
	flags.StringVar(&sc.quic, "quic", "auto", "Override the QUIC probe verdict (auto|on|off)")
	flags.StringVar(&sc.nodeAgent, "node-agent", "auto", "Override the node-agent probe verdict (auto|on|off)")
	flags.StringVar(&sc.scope, "scope", "", "Namespace limiting strategy (all|namespaces|selector|mapped)")
	flags.StringSliceVar(&sc.managedNss, "managed-namespaces", nil, "Namespace list when --scope=namespaces or --scope=mapped")
	_ = cmd.MarkFlagFilename("output")
	_ = cmd.MarkFlagFilename("input")
	sc.rq = daemon.InitRequest(cmd)
	return cmd
}

func (sc *setupCommand) run(cmd *cobra.Command, _ []string) error {
	quic, err := setup.ParseTri(sc.quic)
	if err != nil {
		return err
	}
	nodeAgent, err := setup.ParseTri(sc.nodeAgent)
	if err != nil {
		return err
	}
	flags := cmd.Flags()
	var scope setup.ScopeChoice
	if flags.Changed("scope") {
		if scope, err = setup.ParseScope(sc.scope); err != nil {
			return err
		}
	}
	toStdout := sc.outputFile == "-"
	formatted := output.WantsFormatted(cmd)
	if toStdout && formatted {
		return errcat.User.New("--output - cannot be combined with --format; both claim stdout")
	}
	var inputVals map[string]any
	if sc.inputFile != "" {
		if inputVals, err = setup.LoadInputValues(sc.inputFile); err != nil {
			return err
		}
	}

	facts, err := sc.gatherFacts(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()

	_, isTTY := term.GetFdInfo(cmd.InOrStdin())
	interactive := !sc.nonInteractive && !formatted && isTTY

	promptOut := cmd.OutOrStdout()
	switch {
	case toStdout:
		promptOut = cmd.ErrOrStderr()
	case formatted:
		promptOut = io.Discard
	}
	answers := setup.Answers{
		Attach:            sc.attach,
		Replace:           sc.replace,
		UpgradeManager:    sc.upgradeManager,
		Scope:             scope,
		ManagedNamespaces: sc.managedNss,
		Quic:              quic,
		NodeAgent:         nodeAgent,
	}
	preset := setup.Preset{
		Attach:            flags.Changed("attach"),
		Replace:           flags.Changed("replace"),
		UpgradeManager:    flags.Changed("upgrade-manager"),
		Scope:             flags.Changed("scope"),
		ManagedNamespaces: flags.Changed("managed-namespaces"),
	}
	pins := setup.DerivePins(inputVals)
	pins.ApplyTo(&answers, &preset)

	iv := &setup.Interviewer{
		Facts:          facts,
		In:             cmd.InOrStdin(),
		Out:            promptOut,
		NonInteractive: !interactive,
		Answers:        answers,
		Preset:         preset,
	}
	ivAnswers, err := iv.Interview(ctx)
	if err != nil {
		return err
	}

	var consult setup.ConsultFunc
	if interactive {
		consult = iv.ConsultInput
	}
	proposal, err := setup.RecommendWithInput(facts, ivAnswers, inputVals, consult)
	if err != nil {
		return err
	}
	if err = setup.ValidateValues(facts, proposal.Values); err != nil {
		return err
	}

	if !toStdout {
		sum := &setup.Summary{
			Facts:    facts,
			Answers:  ivAnswers,
			Proposal: proposal,
			Action:   setup.ActionWord(proposal.Action, sc.apply),
		}
		if err = setup.PrintReport(cmd, sum); err != nil {
			return err
		}
	}

	if len(proposal.Values) > 0 {
		switch {
		case toStdout:
			err = setup.WriteValues(cmd.OutOrStdout(), proposal.Values)
		case sc.outputFile != "":
			err = setup.WriteValuesFile(sc.outputFile, proposal.Values)
		}
		if err != nil {
			return err
		}
	}

	if sc.apply && proposal.Action != setup.ActionNone {
		return errcat.User.New("--apply is not yet implemented")
	}
	return nil
}

// gatherFacts connects to the cluster named by the kube flags and runs the
// probes against it, exactly the way the helm commands resolve their cluster
// and manager namespace.
func (sc *setupCommand) gatherFacts(cmd *cobra.Command) (*setup.ClusterFacts, error) {
	if err := sc.rq.CommitFlags(cmd); err != nil {
		return nil, err
	}
	ctx := cmd.Context()
	cr := sc.rq.ConnectRequest
	if cr.ManagerNamespace == "" {
		if ns, ok := cr.KubeFlags["namespace"]; ok {
			cr.ManagerNamespace = ns
		} else {
			cr.ManagerNamespace = "ambassador"
		}
	}

	config, err := k8s.DaemonKubeconfig(ctx, cr)
	if err != nil {
		return nil, err
	}
	cluster, err := k8s.ConnectCluster(cr, config)
	if err != nil {
		return nil, err
	}
	restCfg, err := cluster.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	ki, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}

	prober := &setup.Prober{
		KubeClient:       ki,
		ManagerNamespace: k8s.GetManagerNamespace(cluster),
		ReleaseLookup:    setup.NewReleaseLookup(cluster.Kubeconfig),
	}
	return prober.GatherFacts(cluster)
}
