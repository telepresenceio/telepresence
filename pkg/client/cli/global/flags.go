package global

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	FlagContext  = "context"
	FlagDocker   = "docker"
	FlagNoReport = "no-report"
	FlagOutput   = "output"
	FlagProgress = "progress"
	FlagUse      = "use"
)

var FlagNames = []string{FlagContext, FlagDocker, FlagNoReport, FlagOutput, FlagProgress, FlagUse} //nolint:gochecknoglobals // constant names

func Flags(hasKubeFlags bool) *pflag.FlagSet {
	flags := pflag.NewFlagSet("", 0)
	if !hasKubeFlags {
		// Add deprecated global connect and docker flags.
		flags.String(FlagContext, "", "")
		flags.Lookup(FlagContext).Hidden = true
		flags.Bool(FlagDocker, false, "")
		flags.Lookup(FlagDocker).Hidden = true
	}
	flags.Bool(FlagNoReport, false, "")
	f := flags.Lookup(FlagNoReport)
	f.Hidden = true
	f.Deprecated = "not used"
	flags.String(FlagUse, "", "Match expression that uniquely identifies the daemon container")
	flags.String(FlagOutput, "default", "Set the output format, supported values are 'json', 'yaml', and 'default'")
	flags.String(FlagProgress, "auto", `Set type of progress output (auto, tty, plain, json, quiet)`)
	return flags
}

func SetProgressQuiet(cmd *cobra.Command) {
	pf := cmd.Flag(FlagProgress)
	_ = pf.Value.Set("quiet")
	pf.Changed = true
}
