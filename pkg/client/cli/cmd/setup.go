package cmd

import (
	"fmt"
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
	dryRun         bool
	yes            bool
	nonInteractive bool
	attach         bool
	replace        bool
	upgradeManager bool
	quic           string
	nodeAgent      string
	scope          string
	managedNss     []string
	valuesOut      string
	manifestOut    string
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
report with a generated Helm values document. With --dry-run nothing changes;
otherwise the proposal is applied after a confirmation.`,
		Annotations: map[string]string{
			ann.UpdateCheckFormat: ann.Tel2,
		},
		SilenceUsage:      true,
		SilenceErrors:     true,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE:              sc.run,
	}
	flags := cmd.Flags()
	flags.BoolVar(&sc.dryRun, "dry-run", false, "Probe, interview, and print the proposal; change nothing")
	flags.BoolVar(&sc.yes, "yes", false, "Skip the final apply confirmation")
	flags.BoolVar(&sc.nonInteractive, "non-interactive", false,
		"Never prompt; unanswered questions fall back to flag values or safe defaults")
	flags.BoolVar(&sc.attach, "attach", true, "Clients will attach to workloads (intercept/replace/ingest/wiretap)")
	flags.BoolVar(&sc.replace, "replace", false, "The replace command will be used")
	flags.BoolVar(&sc.upgradeManager, "upgrade-manager", true, "Upgrade an existing, older traffic-manager")
	flags.StringVar(&sc.quic, "quic", "auto", "Override the QUIC probe verdict (auto|on|off)")
	flags.StringVar(&sc.nodeAgent, "node-agent", "auto", "Override the node-agent probe verdict (auto|on|off)")
	flags.StringVar(&sc.scope, "scope", "", "Namespace limiting strategy (all|namespaces|selector|mapped)")
	flags.StringSliceVar(&sc.managedNss, "managed-namespaces", nil, "Namespace list when --scope=namespaces or --scope=mapped")
	flags.StringVar(&sc.valuesOut, "values-out", "", "Write the generated Helm values to this file")
	flags.StringVar(&sc.manifestOut, "manifest-out", "", "Write the workstation state manifest snippet to this file (with --scope=mapped)")
	_ = cmd.MarkFlagFilename("values-out")
	_ = cmd.MarkFlagFilename("manifest-out")
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

	if err = sc.rq.CommitFlags(cmd); err != nil {
		return err
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
		return err
	}
	cluster, err := k8s.ConnectCluster(cr, config)
	if err != nil {
		return err
	}
	mgrNs := k8s.GetManagerNamespace(cluster)
	restCfg, err := cluster.ToRESTConfig()
	if err != nil {
		return err
	}
	ki, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return err
	}

	prober := &setup.Prober{
		KubeClient:       ki,
		ManagerNamespace: mgrNs,
		ReleaseLookup:    setup.NewReleaseLookup(cluster.Kubeconfig),
	}
	facts, err := prober.GatherFacts(cluster)
	if err != nil {
		return err
	}

	formatted := output.WantsFormatted(cmd)
	_, isTTY := term.GetFdInfo(cmd.InOrStdin())
	interactive := !sc.nonInteractive && !formatted && isTTY

	promptOut := cmd.OutOrStdout()
	if formatted {
		promptOut = io.Discard
	}
	iv := &setup.Interviewer{
		Facts:          facts,
		In:             cmd.InOrStdin(),
		Out:            promptOut,
		NonInteractive: !interactive,
		Answers: setup.Answers{
			Attach:            sc.attach,
			Replace:           sc.replace,
			UpgradeManager:    sc.upgradeManager,
			Scope:             scope,
			ManagedNamespaces: sc.managedNss,
			Quic:              quic,
			NodeAgent:         nodeAgent,
		},
		Preset: setup.Preset{
			Attach:            flags.Changed("attach"),
			Replace:           flags.Changed("replace"),
			UpgradeManager:    flags.Changed("upgrade-manager"),
			Scope:             flags.Changed("scope"),
			ManagedNamespaces: flags.Changed("managed-namespaces"),
		},
	}
	answers, err := iv.Interview(ctx)
	if err != nil {
		return err
	}

	proposal, err := setup.Recommend(facts, answers)
	if err != nil {
		return err
	}

	if sc.valuesOut != "" && len(proposal.Values) > 0 {
		if err = setup.WriteValuesFile(sc.valuesOut, proposal.Values); err != nil {
			return err
		}
	}
	manifestWritten := false
	if sc.manifestOut != "" && len(proposal.MappedNamespaces) > 0 {
		if err = setup.WriteManifestSnippet(sc.manifestOut, mgrNs, proposal.MappedNamespaces); err != nil {
			return err
		}
		manifestWritten = true
	}

	sum := &setup.Summary{
		Facts:    facts,
		Answers:  answers,
		Proposal: proposal,
		Action:   setup.ActionWord(proposal.Action, sc.dryRun),
	}
	if err = setup.PrintReport(cmd, sum, !manifestWritten); err != nil {
		return err
	}

	if sc.dryRun || proposal.Action == setup.ActionNone {
		return nil
	}
	if !sc.yes {
		if !interactive {
			return errcat.User.New("a non-interactive setup cannot confirm the apply; re-run with --yes or --dry-run")
		}
		ok, err := setup.Confirm(cmd.InOrStdin(), cmd.OutOrStdout(), fmt.Sprintf("Proceed with %s? [y/N] ", proposal.Action))
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	return errcat.User.New("applying the proposal is not yet implemented; re-run with --dry-run")
}
