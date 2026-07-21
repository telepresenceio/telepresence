package cmd

import (
	"io"
	"time"

	"github.com/moby/term"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/setup"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

type setupCommand struct {
	rq             *daemon.CobraRequest
	nonInteractive bool
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
		"Never prompt; unanswered questions fall back to input-pinned settings or safe defaults")
	_ = cmd.MarkFlagFilename("output")
	_ = cmd.MarkFlagFilename("input")
	sc.rq = daemon.InitKubeRequest(cmd)
	return cmd
}

func (sc *setupCommand) run(cmd *cobra.Command, _ []string) error {
	toStdout := sc.outputFile == "-"
	formatted := output.WantsFormatted(cmd)
	if toStdout && formatted {
		return errcat.User.New("--output - cannot be combined with --format; both claim stdout")
	}
	var inputVals map[string]any
	var err error
	if sc.inputFile != "" {
		if inputVals, err = setup.LoadInputValues(sc.inputFile); err != nil {
			return err
		}
	}

	sc.initProgress(cmd, toStdout)
	pctx := cmd.Context()
	progress.Start(pctx, "Analyzing cluster")
	progress.SetTotal(pctx, len(setup.ProbePhases))
	cl, err := sc.connectAndProbe(cmd)
	progress.Stop(pctx)
	if err != nil {
		return err
	}
	facts := cl.facts
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
	if interactive {
		ioutil.Println(promptOut, setup.Banner(facts))
	}
	var answers setup.Answers
	var preset setup.Preset
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
	proposal, err := setup.RecommendWithInput(facts, ivAnswers, inputVals, consult, sc.apply)
	if err != nil {
		return err
	}
	if err = setup.ValidateValues(facts, proposal.Values, sc.apply); err != nil {
		return err
	}

	return sc.emit(cmd, cl, ivAnswers, proposal, toStdout, formatted)
}

// emit produces everything the command outputs after the decision is made:
// the report (or its structured equivalent), the values document, and the
// apply with its verification.
func (sc *setupCommand) emit(
	cmd *cobra.Command, cl *setupCluster, answers *setup.Answers, proposal *setup.Proposal, toStdout, formatted bool,
) error {
	ctx := cmd.Context()
	facts := cl.facts

	var plannedObjects []string
	if proposal.Action == setup.ActionInstall {
		var err error
		if plannedObjects, err = setup.PlannedObjects(ctx, cl.managerNamespace, proposal.Values); err != nil {
			clog.Debugf(ctx, "unable to render the planned objects: %v", err)
			plannedObjects = nil
		}
	}

	applying := sc.apply && proposal.Action != setup.ActionNone
	var applyOutcome string
	var verification []setup.Note
	var err error
	if applying && formatted {
		if applyOutcome, verification, err = sc.applyAndVerify(cl, proposal, io.Discard); err != nil {
			return err
		}
	}

	if !toStdout {
		sum := &setup.Summary{
			Facts:          facts,
			Answers:        answers,
			Proposal:       proposal,
			Action:         setup.ActionWord(proposal.Action, sc.apply),
			PlannedObjects: plannedObjects,
			ApplyOutcome:   applyOutcome,
			Verification:   verification,
		}
		if err = setup.PrintReport(cmd, sum); err != nil {
			return err
		}
	}

	if len(proposal.Values) > 0 {
		header := setup.ProvenanceHeader(facts, time.Now())
		switch {
		case toStdout:
			err = setup.WriteValues(cmd.OutOrStdout(), proposal.Values, header...)
		case sc.outputFile != "":
			err = setup.WriteValuesFile(sc.outputFile, proposal.Values, header...)
		}
		if err != nil {
			return err
		}
	}

	textOut := cmd.OutOrStdout()
	if toStdout {
		textOut = cmd.ErrOrStderr()
	}
	if applying && !formatted {
		ioutil.Println(textOut, "Applying...")
		if _, verification, err = sc.applyAndVerify(cl, proposal, textOut); err != nil {
			return err
		}
		setup.PrintNotes(textOut, "Verification:", verification)
	}
	return nil
}

// applyAndVerify installs or upgrades the traffic-manager with the proposal
// and runs the post-apply checks.
func (sc *setupCommand) applyAndVerify(cl *setupCluster, p *setup.Proposal, out io.Writer) (string, []setup.Note, error) {
	if err := setup.Apply(cl.cluster, cl.cluster.Kubeconfig, cl.managerNamespace, p, out); err != nil {
		return "", nil, err
	}
	return setup.ApplyOutcome(p.Action), setup.VerifyInstall(cl.cluster, cl.ki, cl.managerNamespace, p.Values), nil
}

// setupCluster is the probed cluster together with the handles the apply and
// verification steps need: the cluster acts as both context and
// RESTClientGetter.
type setupCluster struct {
	facts            *setup.ClusterFacts
	cluster          *k8s.Cluster
	ki               kubernetes.Interface
	managerNamespace string
}

// initProgress installs the progress writer, mirroring how session-bound
// commands resolve the mode; --output - keeps stdout clean by routing
// progress to stderr.
func (sc *setupCommand) initProgress(cmd *cobra.Command, toStdout bool) {
	ctx := cmd.Context()
	mode := progress.ModeAuto
	if output.WantsFormatted(cmd) {
		mode = progress.ModeQuiet
	} else if progress.IsNoOp(ctx) {
		if pf := cmd.Flag(global.FlagProgress); pf != nil && pf.Changed {
			mode = progress.Mode(pf.Value.String())
		} else if me, ok := dos.LookupEnv(ctx, "TELEPRESENCE_PROGRESS"); ok {
			mode = progress.Mode(me)
		}
	}
	out := dos.Stdout(ctx)
	if toStdout {
		out = dos.Stderr(ctx)
	}
	cmd.SetContext(progress.WithContextWriter(ctx, progress.NewWriter(out, dos.Stderr(ctx), mode)))
}

// connectAndProbe connects to the cluster named by the kube flags and runs
// the probes against it, exactly the way the helm commands resolve their
// cluster and manager namespace.
func (sc *setupCommand) connectAndProbe(cmd *cobra.Command) (*setupCluster, error) {
	if err := sc.rq.CommitFlags(cmd); err != nil {
		return nil, err
	}
	ctx := cmd.Context()
	cr := sc.rq.ConnectRequest

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

	cl := &setupCluster{
		cluster:          cluster,
		ki:               ki,
		managerNamespace: k8s.GetManagerNamespace(cluster),
	}
	pctx := ctx
	var lastPhase string
	prober := &setup.Prober{
		KubeClient:       ki,
		ManagerNamespace: cl.managerNamespace,
		Context:          cluster.KubeContext,
		Server:           cluster.Server,
		Progress: func(phase string) {
			if lastPhase != "" {
				progress.Done(progress.WithEventId(pctx, lastPhase))
			}
			lastPhase = phase
			progress.Working(progress.WithEventId(pctx, phase))
		},
		ReleaseLookup: setup.NewReleaseLookup(cluster.Kubeconfig),
	}
	if cl.facts, err = prober.GatherFacts(ctx); err != nil {
		return nil, err
	}
	if lastPhase != "" {
		progress.Done(progress.WithEventId(pctx, lastPhase))
	}
	return cl, nil
}
