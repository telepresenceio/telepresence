package usg

import (
	"context"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Cobra annotation keys controlling anonymous usage reporting.
//
// Reporting is on by default for every command. A command opts out by setting
// AnnTrack to "false"; commands in the untracked set are off by default
// without needing the annotation. The reported topic defaults to
// "cmd." + cmd.Name(), or AnnTopic if explicitly set.
//
// AnnSafeFlags is a comma-separated list of flag names whose *values* are
// safe to report. The flag *names* the user passed are always reported as
// the "flags" entry; values for non-safe flags are never recorded.
const (
	AnnTrack     = "telepresence.io/usg.track"
	AnnTopic     = "telepresence.io/usg.topic"
	AnnSafeFlags = "telepresence.io/usg.safe-flags"
)

type reportCtxKey struct{}

// AttachToRoot installs a PersistentPreRunE hook on the root command that,
// for any tracked descendant (reporting is on by default; see isTracked),
// constructs a Report at Pre time,
// binds it to the command's context, captures the set of flag names the user
// supplied, and wraps the command's RunE so the report is sent on completion
// — including on failure, with "error.*" entries recording the errcat category,
// the chain of Go error types, and any gRPC or HTTP status code of the failure
// (see analyzeError). No error message text is ever recorded.
//
// Cobra's PersistentPostRunE is intentionally not used: cobra skips PostRun
// when RunE returns a non-nil error, which would lose all failure reports.
// Wrapping RunE catches success and failure in a single place.
//
// Existing PersistentPreRunE on the root is preserved and runs first, so that
// config loading (which gates whether reporting is enabled) happens before
// the producer is installed on ctx.
func AttachToRoot(root *cobra.Command) {
	prevPre := root.PersistentPreRunE

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if prevPre != nil {
			if err := prevPre(cmd, args); err != nil {
				return err
			}
		}
		// Install the producer here, after the framework's own PreRun has
		// loaded config — otherwise client.GetConfig would return defaults
		// and the enabled flag wouldn't be honored. When reporting is
		// disabled InstallClient returns ctx unchanged. The CLI doesn't
		// drain the FIFO (only the user daemon does), so we discard the
		// returned sink.
		ctx, _ := InstallClient(cmd.Context())
		cmd.SetContext(ctx)

		if !isTracked(cmd) {
			return nil
		}
		r := New(ctx, topicFor(cmd))
		if r == nil {
			// Reporting disabled — nothing to wire up.
			return nil
		}
		captureFlags(r, cmd)
		cmd.SetContext(context.WithValue(ctx, reportCtxKey{}, r))

		// Wrap RunE so we observe its error and send the report regardless
		// of outcome. Restoration is unnecessary because the process exits
		// after the command returns.
		if orig := cmd.RunE; orig != nil {
			cmd.RunE = func(cmd *cobra.Command, args []string) error {
				err := orig(cmd, args)
				if err != nil {
					analyzeError(err).addTo(r)
				}
				r.Send()
				return err
			}
		} else if origRun := cmd.Run; origRun != nil {
			cmd.Run = func(cmd *cobra.Command, args []string) {
				origRun(cmd, args)
				r.Send()
			}
		} else {
			// No Run/RunE means the command is a group container; report
			// what we have now.
			r.Send()
		}
		return nil
	}
}

// ReportFromContext returns the active Report bound to ctx by AttachToRoot, or
// nil if the command is not being tracked. Safe to call methods on a nil
// result.
func ReportFromContext(ctx context.Context) *Report {
	r, _ := ctx.Value(reportCtxKey{}).(*Report)
	return r
}

// untracked names the leaf commands that do not participate in usage reporting
// even though reporting is on by default: cobra's built-in help and completion,
// and the diagnostic or local-only commands. A command may also opt out
// explicitly with the AnnTrack="false" annotation.
var untracked = map[string]struct{}{ //nolint:gochecknoglobals // effectively a constant
	"completion":      {},
	"gather-logs":     {},
	"help":            {},
	"list":            {},
	"list-contexts":   {},
	"list-namespaces": {},
	"loglevel":        {},
	"status":          {},
	"version":         {},
}

// isTracked reports whether cmd participates in usage reporting. Reporting is on
// by default; a command is excluded when it carries AnnTrack="false", when it is
// the root command (the bare entry point only dispatches to subcommands), when
// it is hidden (internal helpers such as kubeauth are invoked programmatically,
// not by users), or when its name is in the untracked set.
func isTracked(cmd *cobra.Command) bool {
	if v, ok := cmd.Annotations[AnnTrack]; ok {
		return v != "false"
	}
	if cmd.Parent() == nil || cmd.Hidden {
		return false
	}
	_, off := untracked[cmd.Name()]
	return !off
}

func topicFor(cmd *cobra.Command) string {
	if t, ok := cmd.Annotations[AnnTopic]; ok && t != "" {
		return t
	}
	return "cmd." + cmd.Name()
}

func captureFlags(r *Report, cmd *cobra.Command) {
	safe := parseSafeFlags(cmd.Annotations[AnnSafeFlags])
	set := make([]string, 0, 8)
	cmd.Flags().Visit(func(f *pflag.Flag) {
		set = append(set, f.Name)
		if _, ok := safe[f.Name]; ok {
			r.Add("flag."+f.Name, f.Value.String())
		}
	})
	sort.Strings(set)
	if len(set) > 0 {
		r.Add("flags", strings.Join(set, ","))
	}
}

func parseSafeFlags(s string) map[string]struct{} {
	if s == "" {
		return nil
	}
	out := make(map[string]struct{})
	for p := range strings.SplitSeq(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out[p] = struct{}{}
		}
	}
	return out
}
