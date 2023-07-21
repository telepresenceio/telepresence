package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// Here we handle parsing legacy commands, as well as generating Telepresence
// commands from them.  This will make it easier for users to migrate from
// legacy Telepresence.  Note: This isn't exhaustive, but should capture the major
// flags that were used and have a correlated command in Telepresence.

type legacyCommand struct {
	swapDeployment string
	newDeployment  bool
	method         bool
	expose         string
	run            bool
	dockerRun      bool
	runShell       bool
	processCmd     string
	mount          string
	dockerMount    string
	envFile        string
	envJSON        string

	// kubectl-related flags
	context   string
	namespace string

	globalFlags      []string
	unsupportedFlags []string
}

// Unfortunately we have to do our own flag parsing if we see legacy telepresence
// flags because the run command might include something that cobra might detect
// as a flag e.g. --run python3 -m http.server. In python this was handled by
// using argparse.REMAINDER and there is no similar functionality within cobra.
// There is an open ticket to pass unknown flags to the command:
// https://github.com/spf13/cobra/issues/739
// but until that is addressed, we'll do the flag parsing ourselves (which isn't
// the worst because it's a legacy command so the flags won't be growing).
func parseLegacy(args []string) *legacyCommand {
	lc := &legacyCommand{}

	// We don't want to over-index in case somebody has a command that has a
	// flag but doesn't put the value after it.  So we have this helper function
	// to ensure we don't do that.  It may mean the telepresence command at the
	// end fails, but then they'll see the Telepresence error messge and can
	// fix it from there.
	getArg := func(i int) string {
		if len(args) > i {
			return args[i]
		}
		return ""
	}
	kubeFlags := pflag.NewFlagSet("Kubernetes flags", 0)
	kubeConfig := genericclioptions.NewConfigFlags(false)
	kubeConfig.Namespace = nil // "connect", don't take --namespace
	kubeConfig.Context = nil   // --context is global
	kubeConfig.AddFlags(kubeFlags)
Parsing:
	for i, v := range args {
		switch {
		case v == "--swap-deployment" || v == "-s":
			lc.swapDeployment = getArg(i + 1)
		case v == "--new-deployment" || v == "-n":
			lc.newDeployment = true
		case v == "--method" || v == "-m":
			lc.method = true
		case v == "--expose":
			lc.expose = getArg(i + 1)
		case v == "--mount":
			lc.mount = getArg(i + 1)
		case v == "--docker-mount":
			lc.dockerMount = getArg(i + 1)
		case v == "--env-json":
			lc.envJSON = getArg(i + 1)
		case v == "--env-file":
			lc.envFile = getArg(i + 1)
		case v == "--namespace":
			lc.namespace = getArg(i + 1)
		// The three run commands are terminal so we break
		// out of the loop after encountering them.
		// This also means if somebody uses --run and --docker-run
		// in the same command, whichever is first will be used. I don't
		// think this is terrible because I'm not sure how much we need to
		// correct *incorrect* tp1 commands here, but it could be improved
		case v == "--run":
			lc.run = true
			if nxtArg := getArg(i + 1); nxtArg != "" {
				lc.processCmd = strings.Join(args[i+1:], " ")
			}
			break Parsing
		case v == "--docker-run":
			lc.dockerRun = true
			if nxtArg := getArg(i + 1); nxtArg != "" {
				lc.processCmd = strings.Join(args[i+1:], " ")
			}
			break Parsing
		case v == "--run-shell":
			lc.runShell = true
			break Parsing
		case len(v) > 2 && strings.HasPrefix(v, "--"):
			g := v[2:]
			if gf := kubeFlags.Lookup(g); gf != nil {
				lc.globalFlags = append(lc.globalFlags, v)
				if gv := getArg(i + 1); gv != "" && !strings.HasPrefix(gv, "-") {
					lc.globalFlags = append(lc.globalFlags, gv)
				}
				continue Parsing
			}
			lc.unsupportedFlags = append(lc.unsupportedFlags, v)
		}
	}
	return lc
}

// genTPCommand constructs a Telepresence command based on
// the values that are set in the legacyCommand struct.
func (lc *legacyCommand) genTPCommand() (string, error) {
	var cmdSlice []string
	switch {
	// if swapDeployment isn't empty, then our translation is
	// an intercept subcommand
	case lc.swapDeployment != "":
		cmdSlice = append(cmdSlice, "intercept", lc.swapDeployment)
		if lc.expose != "" {
			cmdSlice = append(cmdSlice, "--port", lc.expose)
		}

		if lc.envFile != "" {
			cmdSlice = append(cmdSlice, "--env-file", lc.envFile)
		}

		if lc.envJSON != "" {
			cmdSlice = append(cmdSlice, "--env-json", lc.envJSON)
		}

		if lc.context != "" {
			cmdSlice = append(cmdSlice, "--context", lc.context)
		}

		if lc.namespace != "" {
			cmdSlice = append(cmdSlice, "--namespace", lc.namespace)
		}

		// This should be impossible based on how we currently parse commands.
		// Just putting it here just in case the impossible happens.
		if lc.run && lc.dockerRun {
			return "", errcat.User.New("--run and --docker-run are mutually exclusive")
		}

		if lc.run {
			if lc.mount != "" {
				cmdSlice = append(cmdSlice, "--mount", lc.mount)
			}
		}

		if lc.dockerRun {
			if lc.dockerMount != "" {
				cmdSlice = append(cmdSlice, "--docker-mount", lc.dockerMount)
			}
			cmdSlice = append(cmdSlice, "--docker-run")
		}
		cmdSlice = append(cmdSlice, lc.globalFlags...)

		if lc.processCmd != "" {
			cmdSlice = append(cmdSlice, "--", lc.processCmd)
		}

		if lc.runShell {
			cmdSlice = append(cmdSlice, "--", "bash")
		}
	// If we have a run of some kind without a swapDeployment, then
	// we translate to a connect
	case lc.runShell:
		cmdSlice = append(cmdSlice, "connect")
		cmdSlice = append(cmdSlice, lc.globalFlags...)
		cmdSlice = append(cmdSlice, "--", "bash")
	case lc.run:
		cmdSlice = append(cmdSlice, "connect")
		cmdSlice = append(cmdSlice, lc.globalFlags...)
		cmdSlice = append(cmdSlice, "--", lc.processCmd)
	// Either not a legacyCommand or we don't know how to translate it to Telepresence
	default:
		return "", nil
	}

	return strings.Join(cmdSlice, " "), nil
}

// translateLegacy tries to detect if a legacy Telepresence command was used
// and constructs a Telepresence command from that.
func translateLegacy(args []string) (string, string, *legacyCommand, error) {
	lc := parseLegacy(args)
	tpCmd, err := lc.genTPCommand()
	if err != nil {
		return "", "", lc, err
	}

	// There are certain elements of the telepresence 1 cli that we either
	// don't have a perfect mapping for or want to explicitly let users know
	// about changed behavior.
	msg := ""
	if len(lc.unsupportedFlags) > 0 {
		msg += fmt.Sprintf("The following flags used don't have a direct translation to Telepresence: %s\n",
			strings.Join(lc.unsupportedFlags, " "))
	}
	if lc.method {
		msg += "Telepresence doesn't have proxying methods. You can use --docker-run for container, otherwise it works similarly to vpn-tcp\n"
	}

	if lc.newDeployment {
		msg += "This flag is ignored since Telepresence uses one traffic-manager deployed in the ambassador namespace.\n"
	}
	return tpCmd, msg, lc, nil
}

// perhapsLegacy is like OnlySubcommands but performs some initial check for legacy flags.
func perhapsLegacy(cmd *cobra.Command, args []string) error {
	// If a user is using a flag that is coming from telepresence 1, we try to
	// construct the tp2 command based on their input. If the args passed to
	// telepresence are one of the flags we recognize, we don't want to error
	// out here.
	tp1Flags := []string{"--swap-deployment", "-s", "--run", "--run-shell", "--docker-run", "--help"}
	for _, v := range args {
		for _, flag := range tp1Flags {
			if v == flag {
				return nil
			}
		}
	}
	return OnlySubcommands(cmd, args)
}

// checkLegacy is mostly a wrapper around translateLegacy. The latter
// is separate to make for easier testing.
func checkLegacy(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	tpCmd, msg, lc, err := translateLegacy(args)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	ctx = scout.NewReporter(ctx, "cli")
	scout.Start(ctx)
	defer scout.Close(ctx)

	// Add metadata for the main legacy Telepresence commands so we can
	// track usage and see what legacy commands people are still using.
	if lc.swapDeployment != "" {
		scout.SetMetadatum(ctx, "swap_deployment", true)
	}
	if lc.run {
		scout.SetMetadatum(ctx, "run", true)
	}
	if lc.dockerRun {
		scout.SetMetadatum(ctx, "docker_run", true)
	}
	if lc.runShell {
		scout.SetMetadatum(ctx, "run_shell", true)
	}
	if lc.unsupportedFlags != nil {
		scout.SetMetadatum(ctx, "unsupported_flags", lc.unsupportedFlags)
	}
	scout.Report(ctx, "Used legacy syntax")

	// Generate output to user letting them know legacy Telepresence was used,
	// what the Telepresence command is, and runs it.
	if tpCmd != "" {
		fmt.Fprintf(cmd.OutOrStderr(), "Legacy Telepresence command used\n")

		if msg != "" {
			fmt.Fprintln(cmd.OutOrStderr(), msg)
		}

		fmt.Fprintf(cmd.OutOrStderr(), "Command roughly translates to the following in Telepresence:\ntelepresence %s\n", tpCmd)
		ctx := cmd.Context()
		fmt.Fprintln(cmd.OutOrStderr(), "running...")
		newCmd := Telepresence(ctx)
		newCmd.SetArgs(strings.Split(tpCmd, " "))
		newCmd.SetOut(cmd.OutOrStderr())
		newCmd.SetErr(cmd.OutOrStderr())
		if err := newCmd.ExecuteContext(ctx); err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), err)
		}
	}
	return nil
}
