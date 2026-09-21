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
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
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

The command probes the cluster (privileges, QUIC and node-agent viability,
webhook creation, namespace scale, and any existing installation), then asks
only the questions the findings leave open, including whether to enforce
caller authentication, the required grant, an external control endpoint, and
legacy client access, and prints a report with the values. At least one of
--output and --apply is required: --output writes the values file (--output -
for a read-only run that only prints them), and --apply installs or upgrades
the traffic-manager; passing both writes the file and applies it.`,
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
	if sc.outputFile == "" && !sc.apply {
		return errcat.User.New("specify --output <file> (or --output -) to write the values, --apply to install them, or both")
	}
	toStdout := sc.outputFile == "-"
	formatted := output.WantsFormatted(cmd)
	if toStdout && formatted {
		return errcat.User.New("--output - cannot be combined with --format; both claim stdout")
	}
	inputVals := &helm.Values{}
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
	if err := facts.Release.ReadableValues(); err != nil {
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
	if interactive {
		ioutil.Println(promptOut, facts.Banner())
	}
	var answers setup.Answers
	var preset setup.Preset
	setup.PinAnswers(inputVals, &answers, &preset)

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
	if proposal.Values != nil {
		if err = facts.ValidateValues(proposal.Values, sc.apply); err != nil {
			return err
		}
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

	if proposal.Values != nil {
		header := facts.ProvenanceHeader(time.Now())
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
	return setup.ApplyOutcome(p.Action), setup.VerifyInstall(cl.cluster, cl.ki, cl.managerNamespace, p.Values, cl.facts.ClientAuth), nil
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
	// The privileges sweep issues dozens of parallel SubjectAccessReviews;
	// the default client-side rate limit (5 QPS, burst 10) would throttle it.
	restCfg.QPS = 50
	restCfg.Burst = 100
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
	bearer, x509 := k8s.ClientAuthMethods(cluster.Kubeconfig)
	prober := &setup.Prober{
		KubeClient:       ki,
		ManagerNamespace: cl.managerNamespace,
		Context:          cluster.KubeContext,
		Server:           cluster.Server,
		ClientAuth:       setup.ClientAuthFacts{Bearer: bearer, X509: x509},
		Progress: func(phase string) {
			progress.Working(progress.WithEventId(pctx, phase))
		},
		// Only a Done event closes a phase's row and replaces its status text; the
		// verdict is already spelled out in the summary.
		Outcome: func(phase string, _ setup.Verdict, summary string) {
			progress.PrintDone(progress.WithEventId(pctx, phase), summary)
		},
		ReleaseLookup: setup.NewReleaseLookup(cluster.Kubeconfig),
	}
	if cl.facts, err = prober.GatherFacts(ctx); err != nil {
		return nil, err
	}
	return cl, nil
}
